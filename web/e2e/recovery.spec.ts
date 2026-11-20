import { expect, test } from "@playwright/test";
import { signIn } from "./helpers";

test("overdue and needs-info work can be found, filtered and recovered", async ({ page }) => {
  await signIn(page, "Ana Demo");

  // Overdue filter (seed: unquoted high-priority request past its SLA, and a
  // quote whose follow-up time has passed). Filters survive a refresh.
  await page.getByLabel("Overdue only").click();
  await expect(page).toHaveURL(/overdue=true/);
  await expect(page.getByTestId("row-CD-1002")).toBeVisible();
  await expect(page.getByTestId("row-CD-1004")).toBeVisible();
  await expect(page.getByTestId("row-CD-1001")).toHaveCount(0);
  await page.reload();
  await expect(page.getByLabel("Overdue only")).toBeChecked();
  await expect(page.locator("tbody tr")).toHaveCount(2);

  // Owner + search filters.
  await page.getByLabel("Overdue only").click();
  await expect(page).not.toHaveURL(/overdue/);
  await page.getByLabel("Owner").selectOption("none");
  await expect(page.getByTestId("row-CD-1001")).toBeVisible();
  await expect(page.getByTestId("row-CD-1003")).toHaveCount(0);
  await page.getByLabel("Owner").selectOption("");
  await page.getByLabel("Search").fill("globex");
  await page.getByRole("button", { name: "Search" }).click();
  await expect(page.locator("tbody tr")).toHaveCount(2);
  await page.getByLabel("Search").fill("");
  await page.getByRole("button", { name: "Search" }).click();

  // Invalid transition requirements surface as field errors.
  await page.getByRole("link", { name: "CD-1002" }).click();
  await expect(page.getByTestId("reference")).toHaveText("CD-1002");
  const move = page.getByRole("region", { name: "Move request" });
  await move.getByRole("button", { name: "Quoted" }).click();
  await move.getByLabel("Amount").fill("10");
  await move.getByLabel("Currency").fill("us");
  await move.getByRole("button", { name: "Confirm: Quoted" }).click();
  await expect(page.getByTestId("error")).toContainText("quote.currency");

  // Missing information: needs_info requires a reason, then back to new.
  await move.getByRole("button", { name: "Needs info" }).click();
  await move.getByLabel("Reason").fill("Need container size (20ft or 40ft)");
  await move.getByRole("button", { name: "Confirm: Needs info" }).click();
  await expect(page.getByTestId("state")).toHaveText("Needs info");
  await move.getByRole("button", { name: "Back to New" }).click();
  await move.getByLabel("Reason").fill("Customer confirmed 40ft");
  await move.getByRole("button", { name: "Confirm: New" }).click();
  await expect(page.getByTestId("state")).toHaveText("New");
  await expect(page.getByTestId("event").last()).toContainText("Reason: Customer confirmed 40ft");

  // Notes are rendered as text, never as HTML.
  const xss = `<img src=x onerror="window.__xss=1"><b>bold?</b>`;
  await page.getByRole("form", { name: "Add note" }).getByLabel("Note").fill(xss);
  await page.getByRole("button", { name: "Add note" }).click();
  await expect(page.getByTestId("event").last()).toContainText(xss);
  expect(await page.evaluate(() => (window as unknown as { __xss?: number }).__xss)).toBeUndefined();
  await expect(page.locator(".timeline img")).toHaveCount(0);
});

test("a stale version is rejected with a conflict and can be reloaded", async ({ browser }) => {
  const a = await (await browser.newContext()).newPage();
  const b = await (await browser.newContext()).newPage();
  await signIn(a, "Ana Demo");
  await signIn(b, "Bruno Sample");
  for (const p of [a, b]) {
    await p.getByRole("link", { name: "CD-1001" }).click();
    await expect(p.getByTestId("reference")).toHaveText("CD-1001");
    await expect(p.getByTestId("state")).toHaveText("New");
  }
  const moveA = a.getByRole("region", { name: "Move request" });
  await moveA.getByRole("button", { name: "Needs info" }).click();
  await moveA.getByLabel("Reason").fill("Missing pallet count");
  await moveA.getByRole("button", { name: "Confirm: Needs info" }).click();
  await expect(a.getByTestId("state")).toHaveText("Needs info");

  const assignB = b.getByRole("form", { name: "Assignment" });
  await assignB.getByLabel("Priority").selectOption("urgent");
  await assignB.getByRole("button", { name: "Save assignment" }).click();
  await expect(b.getByTestId("error")).toContainText("changed by someone else");
  await b.getByRole("button", { name: "Reload latest version" }).click();
  await expect(b.getByTestId("state")).toHaveText("Needs info");
  await expect(b.getByTestId("priority")).toHaveText("normal");
});

test("viewer can read but not change anything", async ({ page }) => {
  await signIn(page, "Dario Viewer");
  await expect(page.getByRole("link", { name: "New request" })).toHaveCount(0);
  await expect(page.getByRole("link", { name: "Export CSV" })).toHaveCount(0);
  await page.getByRole("link", { name: "CD-1004" }).click();
  await expect(page.getByTestId("reference")).toHaveText("CD-1004");
  await expect(page.getByTestId("read-only")).toBeVisible();
  await expect(page.getByRole("region", { name: "Move request" })).toHaveCount(0);
  await expect(page.getByRole("form", { name: "Assignment" })).toHaveCount(0);
  await expect(page.getByRole("form", { name: "Add note" })).toHaveCount(0);
  await page.goto("/settings");
  await expect(page.getByText("Only admins can change the policy.")).toBeVisible();
  // The API enforces the same rule if the UI is bypassed.
  const res = await page.request.post("/api/v1/shipments", { data: { account_name: "x" } });
  expect(res.status()).toBe(403);
});

test("tenants are isolated and admins can change the SLA policy", async ({ page }) => {
  await signIn(page, "Erin Other-Tenant");
  await expect(page.getByTestId("row-SH-1001")).toBeVisible();
  await expect(page.locator("tbody tr")).toHaveCount(1);
  await page.getByRole("button", { name: "Sign out" }).click();

  await signIn(page, "Carla Admin");
  await expect(page.getByText("Hidden Tenant Account")).toHaveCount(0);
  await page.getByRole("link", { name: "SLA policy" }).click();
  await page.getByLabel("urgent (hours)").fill("3");
  await page.getByRole("button", { name: "Save policy" }).click();
  await expect(page.getByRole("status")).toHaveText("Saved");
  await page.reload();
  await expect(page.getByLabel("urgent (hours)")).toHaveValue("3");
});

test("CSV export neutralizes spreadsheet formulas", async ({ page }) => {
  await signIn(page, "Ana Demo");
  const res = await page.request.post("/api/v1/shipments", {
    data: { account_name: "=1+2 (formula test)", contact_label: "x", origin: "A (demo)", destination: "B (demo)",
      cargo_type: "general", required_date: new Date(Date.now() + 5 * 86400_000).toISOString().slice(0, 10) },
  });
  expect(res.status()).toBe(201);
  await page.goto("/?q=formula");
  const download = page.waitForEvent("download");
  await page.getByRole("link", { name: "Export CSV" }).click();
  const file = await (await download).path();
  const { readFileSync } = await import("node:fs");
  const csv = readFileSync(file, "utf8");
  expect(csv.split("\n")[0]).toBe("reference,state,priority,owner,account_name,origin,destination,cargo_type,required_date,next_action_at,due_at,created_at");
  expect(csv).toContain("'=1+2 (formula test)");
});
