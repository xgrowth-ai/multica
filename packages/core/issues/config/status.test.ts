import { describe, expect, it } from "vitest";
import { ALL_STATUSES, STATUS_CONFIG, STATUS_ORDER } from "./status";

describe("issue status configuration", () => {
  it("keeps built-in statuses separate from lifecycle categories", () => {
    expect(STATUS_ORDER).toEqual(["unstarted", "started", "done", "closed"]);
    expect(ALL_STATUSES).toEqual(STATUS_ORDER);
    expect(STATUS_CONFIG.started.label).toBe("Started");
  });
});
