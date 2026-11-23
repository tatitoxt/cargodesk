import { expect, test } from "@playwright/test";
import { inDays, localInput, signIn } from "./helpers";

test("coordinator takes a request from creation to delivery and audits the timeline", async ({ page }) => {
  await signIn(page, "Ana Demo");

  await page.getByRole("link", { name: "New request" }).click();
  const form = page.getByRole("form", { name: "New request" });
  await form.getByLabel("Account").fill("E2E Synthetic Bakery");
  await form.getByLabel("Contact").fill("Night shift lead (demo)");
  await form.getByLabel("Origin").fill("Bakery Plant (demo)");
  await form.getByLabel("Destination").fill("Cafe Chain Hub (demo)");
  await form.getByLabel("Cargo type").selectOption("refrigerated");
  await form.getByLabel("Required date").fill(inDays(3));
  await form.getByLabel("Priority").selectOption("high");
  await form.getByRole("button", { name: "Create request" }).click();

  await expect(page.getByTestId("state")).toHaveText("New");
  await expect(page.getByTestId("sla")).toHaveText("On track");
  const reference = (await page.getByTestId("reference").textContent())!;
  expect(reference).toMatch(/^CD-\d+$/);

  // Assign owner and next action.
  const assignment = page.getByRole("form", { name: "Assignment" });
  await assignment.getByLabel("Owner").selectOption({ label: "Bruno Sample" });
  await assignment.getByLabel("Next action", { exact: true }).fill(localInput(2));
  await assignment.getByLabel("Next action note").fill("Send quote");
  await assignment.getByRole("button", { name: "Save assignment" }).click();
  await expect(page.getByTestId("owner")).toHaveText("Bruno Sample");

  const move = page.getByRole("region", { name: "Move request" });
  // Quote.
  await move.getByRole("button", { name: "Quoted" }).click();
  await move.getByLabel("Amount").fill("1250.50");
  await move.getByRole("button", { name: "Confirm: Quoted" }).click();
  await expect(page.getByTestId("state")).toHaveText("Quoted");
  await expect(page.getByTestId("sla")).toHaveText("Met");
  await expect(page.getByText("$1,250.50 · open")).toBeVisible();

  // Accept.
  await move.getByRole("button", { name: "Accepted" }).click();
  await move.getByRole("button", { name: "Confirm: Accepted" }).click();
  await expect(page.getByTestId("state")).toHaveText("Accepted");

  // Schedule.
  await move.getByRole("button", { name: "Scheduled" }).click();
  await move.getByLabel("Pickup at").fill(localInput(24));
  await move.getByLabel("Carrier").fill("Carrier E2E (demo)");
  await move.getByRole("button", { name: "Confirm: Scheduled" }).click();
  await expect(page.getByTestId("state")).toHaveText("Scheduled");

  // In transit, then delivered with evidence.
  await move.getByRole("button", { name: "In transit" }).click();
  await move.getByRole("button", { name: "Confirm: In transit" }).click();
  await expect(page.getByTestId("state")).toHaveText("In transit");
  await move.getByRole("button", { name: "Delivered" }).click();
  await move.getByLabel("Received by").fill("Dock clerk (demo)");
  await move.getByLabel("Delivered at").fill(localInput(-0.1));
  await move.getByLabel("Evidence note").fill("Signed note DN-E2E (synthetic)");
  await move.getByRole("button", { name: "Confirm: Delivered" }).click();
  await expect(page.getByTestId("state")).toHaveText("Delivered");
  await expect(page.getByTestId("evidence")).toContainText("Dock clerk (demo)");
  await expect(page.getByRole("region", { name: "Move request" })).toHaveCount(0);

  // The timeline records every step with its actor, in order.
  const events = page.getByTestId("event");
  await expect(events).toHaveCount(7);
  await expect(events.nth(0)).toContainText("Request created");
  await expect(events.nth(1)).toContainText("owner: nobody → Bruno Sample");
  await expect(events.nth(2)).toContainText("New → Quoted ($1,250.50)");
  await expect(events.nth(3)).toContainText("Quoted → Accepted");
  await expect(events.nth(4)).toContainText("Accepted → Scheduled (carrier Carrier E2E (demo))");
  await expect(events.nth(5)).toContainText("Scheduled → In transit");
  await expect(events.nth(6)).toContainText("In transit → Delivered (received by Dock clerk (demo))");
  await expect(events.nth(6)).toContainText("Ana Demo");

  // Refresh keeps the persisted state.
  await page.reload();
  await expect(page.getByTestId("state")).toHaveText("Delivered");
  await expect(page.getByTestId("event")).toHaveCount(7);

  // It shows on the board with the delivered filter.
  await page.getByRole("link", { name: "← Requests" }).click();
  await expect(page.getByRole("heading", { name: "Requests" })).toBeVisible();
  await page.getByLabel("State").selectOption("delivered");
  await expect(page.getByTestId(`row-${reference}`)).toBeVisible();
});
