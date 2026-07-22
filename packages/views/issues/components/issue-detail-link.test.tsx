import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useIssueDetailOpenStore } from "@multica/core/issues/stores";
import { NavigationProvider, type NavigationAdapter } from "../../navigation";
import { IssueDetailLink } from "./issue-detail-link";

function renderLink(adapter: NavigationAdapter) {
  return render(
    <NavigationProvider value={adapter}>
      <IssueDetailLink href="/acme/issues/issue-1" issueId="issue-1">
        MUL-1
      </IssueDetailLink>
    </NavigationProvider>,
  );
}

function adapter(): NavigationAdapter {
  return {
    push: vi.fn(),
    replace: vi.fn(),
    back: vi.fn(),
    pathname: "/acme/issues",
    searchParams: new URLSearchParams(),
    openInNewTab: vi.fn(),
    getShareableUrl: (path) => path,
  };
}

describe("IssueDetailLink", () => {
  beforeEach(() => {
    useIssueDetailOpenStore.setState({
      openInDrawer: true,
      drawerIssueId: null,
    });
  });

  it("opens a plain click in the issue drawer", async () => {
    const navigation = adapter();
    renderLink(navigation);

    await userEvent.click(screen.getByRole("link", { name: "MUL-1" }));

    expect(useIssueDetailOpenStore.getState().drawerIssueId).toBe("issue-1");
    expect(navigation.push).not.toHaveBeenCalled();
  });

  it("uses normal navigation when the preference is disabled", async () => {
    useIssueDetailOpenStore.setState({ openInDrawer: false });
    const navigation = adapter();
    renderLink(navigation);

    await userEvent.click(screen.getByRole("link", { name: "MUL-1" }));

    expect(useIssueDetailOpenStore.getState().drawerIssueId).toBeNull();
    expect(navigation.push).toHaveBeenCalledWith("/acme/issues/issue-1");
  });

  it("preserves modifier-click new-tab behavior", async () => {
    const navigation = adapter();
    renderLink(navigation);

    fireEvent.click(screen.getByRole("link", { name: "MUL-1" }), {
      metaKey: true,
    });

    expect(useIssueDetailOpenStore.getState().drawerIssueId).toBeNull();
    expect(navigation.openInNewTab).toHaveBeenCalledWith(
      "/acme/issues/issue-1",
      undefined,
    );
  });
});
