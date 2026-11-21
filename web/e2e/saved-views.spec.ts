import { expect, test } from "@playwright/test";
import { signIn } from "./helpers";

test("coordinator saves, applies and deletes a board view", async ({ page }) => {
  await signIn(page, "Ana Demo");
  await page.goto("/");

  // A filter combo becomes a named view.
  await page.getByLabel("State").selectOption("new");
  await page.getByRole("button", { name: "Save view" }).click();
  const saveForm = page.getByRole("form", { name: "Save view" });
  await saveForm.getByLabel("View name").fill("E2E new only");
  await saveForm.getByRole("button", { name: "Save current filters" }).click();

  // The view is applied (URL carries its filters) and appears in the picker.
  await expect(page).toHaveURL(/state=new/);
  await expect(page.getByLabel("Saved view")).toHaveValue(/[0-9a-f-]{36}/);

  // Clearing the filter deselects the view; picking it reapplies it.
  await page.getByLabel("State").selectOption("");
  await expect(page).not.toHaveURL(/state=new/);
  await page.getByLabel("Saved view").selectOption({ label: "E2E new only" });
  await expect(page).toHaveURL(/state=new/);

  // Deleting removes it for good — the picker disappears when no views remain.
  await page.getByRole("button", { name: "Delete view" }).click();
  await expect(page.getByLabel("Saved view")).toBeHidden();
});
