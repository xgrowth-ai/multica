import { describe, expect, it } from "vitest";
import { ALL_STATUSES, STATUS_CONFIG, STATUS_ORDER } from "./status";

describe("issue status configuration", () => {
  it("places pending verification between review and done", () => {
    const expected = [
      "backlog",
      "todo",
      "in_progress",
      "in_review",
      "pending_verification",
      "done",
      "blocked",
      "cancelled",
    ];

    expect(STATUS_ORDER).toEqual(expected);
    expect(ALL_STATUSES).toEqual(expected);
    expect(STATUS_CONFIG.pending_verification.label).toBe(
      "Pending Verification",
    );
  });
});
