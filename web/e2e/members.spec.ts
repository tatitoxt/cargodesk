import { expect, test } from "@playwright/test";
import { signIn } from "./helpers";

test("admin invites, re-roles and removes a member", async ({ page }) => {
  await signIn(page, "Carla Admin");
  await page.goto("/members");

  const roster = page.getByRole("table", { name: "Member roster" });
  await expect(roster).toContainText("ana.coordinator@cargodesk.example");

  // Invite a viewer.
  const form = page.getByRole("form", { name: "Invite member" });
  await form.getByLabel("Email").fill("e2e.member@cargodesk.example");
  await form.getByLabel("Name").fill("E2E Member");
  await form.getByLabel("Role").selectOption("viewer");
  await form.getByRole("button", { name: "Invite member" }).click();
  const row = roster.getByRole("row").filter({ hasText: "e2e.member@cargodesk.example" });
  await expect(row).toContainText("E2E Member");

  // Promote to coordinator.
  await row.getByLabel("Role for e2e.member@cargodesk.example").selectOption("coordinator");
  await expect(row.getByLabel("Role for e2e.member@cargodesk.example")).toHaveValue("coordinator");

  // Remove them (confirm dialog).
  page.on("dialog", (d) => d.accept());
  await row.getByRole("button", { name: "Remove" }).click();
  await expect(roster.getByRole("row").filter({ hasText: "e2e.member@cargodesk.example" })).toHaveCount(0);
});

test("duplicate invite shows the API error", async ({ page }) => {
  await signIn(page, "Carla Admin");
  await page.goto("/members");
  const form = page.getByRole("form", { name: "Invite member" });
  await form.getByLabel("Email").fill("ana.coordinator@cargodesk.example");
  await form.getByLabel("Name").fill("Duplicate");
  await form.getByRole("button", { name: "Invite member" }).click();
  await expect(page.getByTestId("error")).toBeVisible();
});

test("non-admin sees the roster without member controls", async ({ page }) => {
  await signIn(page, "Ana Demo");
  await page.goto("/members");
  await expect(page.getByRole("table", { name: "Member roster" })).toContainText("carla.admin@cargodesk.example");
  await expect(page.getByRole("form", { name: "Invite member" })).toHaveCount(0);
  await expect(page.getByRole("button", { name: "Remove" })).toHaveCount(0);
});
