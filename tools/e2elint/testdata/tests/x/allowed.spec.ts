// e2elint:allow-clock
test("c", async ({ clock }) => { await clock.advance("1h"); });
