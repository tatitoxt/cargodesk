import { expect, test } from "@playwright/test";

test("self-service signup provisions an org and lands on its board", async ({ page }) => {
  await page.goto("/login");
  await page.getByRole("link", { name: "Create an organization" }).click();

  await expect(page.getByRole("heading", { name: "Create your organization" })).toBeVisible();
  const form = page.getByRole("form", { name: "Organization sign-up" });
  const slug = `e2e-${Date.now().toString(36)}`;
  await form.getByLabel("Organization slug").fill(slug);
  await form.getByLabel("Organization name").fill("E2E Freight");
  await form.getByLabel("Reference prefix").fill("EF");
  await form.getByLabel("Your name").fill("E2E Owner");
  await form.getByLabel("Work email").fill(`owner@${slug}.example`);
  await form.getByLabel("Password").fill("e2e-signup-pass");
  await form.getByRole("button", { name: "Create organization" }).click();

  // Signed in straight onto the fresh org's board — empty, proving isolation.
  await expect(page.getByTestId("current-user")).toContainText("E2E Owner");
  await expect(page.getByRole("heading", { name: "Requests" })).toBeVisible();
  await expect(page.getByTestId("empty")).toBeVisible();
});
