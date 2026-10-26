test("b", { tag: "@isolation:getThing" }, async ({ clock, page }) => { await clock.runJob("x"); await page.waitForTimeout(100); });
