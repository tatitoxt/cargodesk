import { ApiError } from "@/lib/api";

export function ErrorBox({ error }: { error: unknown }) {
  if (!error) return null;
  if (error instanceof ApiError) {
    return (
      <div className="error" role="alert" data-testid="error">
        {error.message}
        {error.fields.length > 0 && (
          <ul>
            {error.fields.map((f) => (
              <li key={f.field + f.message}>
                <code>{f.field}</code>: {f.message}
              </li>
            ))}
          </ul>
        )}
      </div>
    );
  }
  return <div className="error" role="alert" data-testid="error">{String(error)}</div>;
}
