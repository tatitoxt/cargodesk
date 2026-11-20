package workflow

import (
	"errors"
	"slices"
	"testing"
	"time"
)

var now = time.Date(2026, 3, 10, 15, 0, 0, 0, time.UTC)

func ptr[T any](v T) *T { return &v }

// expected is the transition table from docs/spec/operations-workflow.md.
var expected = map[[2]State]bool{
	{New, NeedsInfo}: true, {New, Quoted}: true, {New, Cancelled}: true, {New, Lost}: true,
	{NeedsInfo, New}: true, {NeedsInfo, Quoted}: true, {NeedsInfo, Cancelled}: true, {NeedsInfo, Lost}: true,
	{Quoted, Accepted}: true, {Quoted, NeedsInfo}: true, {Quoted, Cancelled}: true, {Quoted, Lost}: true,
	{Accepted, Scheduled}: true, {Accepted, Cancelled}: true,
	{Scheduled, InTransit}: true, {Scheduled, Cancelled}: true,
	{InTransit, Delivered}: true,
}

// validInput returns a complete input and the facts for a transition to `to`.
func validInput(to State) (TransitionInput, Facts) {
	in := TransitionInput{To: to}
	f := Facts{Now: now}
	switch to {
	case NeedsInfo, New, Cancelled, Lost:
		in.Reason = "synthetic reason"
	case Quoted:
		in.Quote = &QuoteInput{AmountCents: 125000, Currency: "USD", ValidUntil: ptr(now.Add(72 * time.Hour))}
	case Accepted:
		f.HasOpenQuote = true
	case Scheduled:
		f.HasAcceptedQuote = true
		in.PickupAt = ptr(now.Add(24 * time.Hour))
		in.Carrier = "Example Carrier Unit 7"
	case Delivered:
		in.Evidence = &EvidenceInput{ReceivedBy: "Dock clerk (fictitious)", DeliveredAt: ptr(now.Add(-time.Hour))}
	}
	return in, f
}

func TestTransitionMatrix(t *testing.T) {
	for _, from := range AllStates {
		for _, to := range AllStates {
			t.Run(string(from)+"->"+string(to), func(t *testing.T) {
				want := expected[[2]State{from, to}]
				if got := CanTransition(from, to); got != want {
					t.Fatalf("CanTransition = %v, want %v", got, want)
				}
				in, f := validInput(to)
				err := Validate(from, in, f)
				if want && err != nil {
					t.Fatalf("valid input rejected: %v", err)
				}
				if !want {
					var ite *InvalidTransitionError
					if !errors.As(err, &ite) {
						t.Fatalf("want InvalidTransitionError, got %v", err)
					}
					if ite.From != from || ite.To != to || !slices.Equal(ite.Allowed, Allowed(from)) {
						t.Fatalf("error lacks context: %+v", ite)
					}
				}
			})
		}
	}
}

func TestTerminalStates(t *testing.T) {
	for _, s := range AllStates {
		want := s == Delivered || s == Cancelled || s == Lost
		if s.Terminal() != want {
			t.Errorf("%s.Terminal() = %v, want %v", s, s.Terminal(), want)
		}
	}
	if State("archived").Valid() {
		t.Error("unknown state must be invalid")
	}
}

func TestNewToDeliveredIsRejectedWithExplanation(t *testing.T) {
	err := Validate(New, TransitionInput{To: Delivered}, Facts{Now: now})
	var ite *InvalidTransitionError
	if !errors.As(err, &ite) {
		t.Fatalf("got %v", err)
	}
	if ite.Error() != "transition new → delivered is not allowed" {
		t.Fatalf("message = %q", ite.Error())
	}
	if !slices.Equal(ite.Allowed, []State{NeedsInfo, Quoted, Cancelled, Lost}) {
		t.Fatalf("allowed = %v", ite.Allowed)
	}
}

func fieldsOf(t *testing.T, err error) []string {
	t.Helper()
	var re *RequirementsError
	if !errors.As(err, &re) {
		t.Fatalf("want RequirementsError, got %v", err)
	}
	out := make([]string, len(re.Fields))
	for i, f := range re.Fields {
		out[i] = f.Field
	}
	return out
}

func TestTransitionRequirements(t *testing.T) {
	cases := []struct {
		name   string
		from   State
		in     TransitionInput
		facts  Facts
		fields []string
	}{
		{"needs_info needs reason", New, TransitionInput{To: NeedsInfo, Reason: "  "}, Facts{}, []string{"reason"}},
		{"back to new needs reason", NeedsInfo, TransitionInput{To: New}, Facts{}, []string{"reason"}},
		{"quoted needs quote", New, TransitionInput{To: Quoted}, Facts{}, []string{"quote"}},
		{"quote fields validated", NeedsInfo, TransitionInput{To: Quoted, Quote: &QuoteInput{AmountCents: 0, Currency: "usd", ValidUntil: ptr(now.Add(-time.Hour))}}, Facts{}, []string{"quote.amount_cents", "quote.currency", "quote.valid_until"}},
		{"accept needs open quote", Quoted, TransitionInput{To: Accepted}, Facts{}, []string{"quote"}},
		{"schedule needs accepted quote, pickup and carrier", Accepted, TransitionInput{To: Scheduled}, Facts{}, []string{"quote", "pickup_at", "carrier"}},
		{"schedule with accepted quote still needs pickup", Accepted, TransitionInput{To: Scheduled, Carrier: "Unit 1"}, Facts{HasAcceptedQuote: true}, []string{"pickup_at"}},
		{"deliver needs evidence", InTransit, TransitionInput{To: Delivered}, Facts{}, []string{"evidence"}},
		{"evidence fields", InTransit, TransitionInput{To: Delivered, Evidence: &EvidenceInput{DeliveredAt: ptr(now.Add(time.Hour))}}, Facts{}, []string{"evidence.received_by", "evidence.delivered_at"}},
		{"cancel needs reason", Scheduled, TransitionInput{To: Cancelled}, Facts{}, []string{"reason"}},
		{"lost needs reason", Quoted, TransitionInput{To: Lost}, Facts{}, []string{"reason"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			c.facts.Now = now
			got := fieldsOf(t, Validate(c.from, c.in, c.facts))
			if !slices.Equal(got, c.fields) {
				t.Fatalf("fields = %v, want %v", got, c.fields)
			}
		})
	}
}

func TestReasonLengthLimit(t *testing.T) {
	long := make([]byte, maxTextLen+1)
	for i := range long {
		long[i] = 'x'
	}
	got := fieldsOf(t, Validate(New, TransitionInput{To: Cancelled, Reason: string(long)}, Facts{Now: now}))
	if !slices.Equal(got, []string{"reason"}) {
		t.Fatalf("fields = %v", got)
	}
}

func TestAwaitingQuote(t *testing.T) {
	for _, s := range AllStates {
		want := s == New || s == NeedsInfo
		if s.AwaitingQuote() != want {
			t.Errorf("%s.AwaitingQuote() = %v", s, s.AwaitingQuote())
		}
	}
}
