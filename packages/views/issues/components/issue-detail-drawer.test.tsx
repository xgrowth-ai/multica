import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import { I18nProvider } from "@multica/core/i18n/react";
import { useIssueDetailOpenStore } from "@multica/core/issues/stores";
import enIssues from "../../locales/en/issues.json";
import {
  NavigationProvider,
  type NavigationAdapter,
} from "../../navigation";

vi.mock("./issue-detail", () => ({
  IssueDetail: ({
    issueId,
    defaultSidebarOpen,
    layoutId,
  }: {
    issueId: string;
    defaultSidebarOpen?: boolean;
    layoutId?: string;
  }) => (
    <div
      data-testid="issue-detail"
      data-issue-id={issueId}
      data-sidebar-open={String(defaultSidebarOpen)}
      data-layout-id={layoutId}
    />
  ),
}));

import { IssueDetailDrawer } from "./issue-detail-drawer";

const navigation: NavigationAdapter = {
  push: vi.fn(),
  replace: vi.fn(),
  back: vi.fn(),
  pathname: "/acme/issues",
  searchParams: new URLSearchParams(),
  getShareableUrl: (path) => path,
};

function renderDrawer() {
  return render(
    <I18nProvider locale="en" resources={{ en: { issues: enIssues } }}>
      <NavigationProvider value={navigation}>
        <IssueDetailDrawer />
      </NavigationProvider>
    </I18nProvider>,
  );
}

describe("IssueDetailDrawer", () => {
  beforeEach(() => {
    useIssueDetailOpenStore.setState({ drawerIssueId: "issue-1" });
  });

  it("opens a twice-wider full-detail workspace with the property sidebar visible", () => {
    renderDrawer();

    const drawer = screen.getByRole("dialog");
    expect(drawer).toHaveClass(
      "data-[side=right]:w-[min(2800px,calc(100vw-1.5rem))]",
      "data-[side=right]:sm:max-w-none",
    );
    expect(drawer).not.toHaveClass("data-[side=right]:w-3/4");
    expect(drawer).not.toHaveClass("data-[side=right]:sm:max-w-sm");
    expect(screen.getByTestId("issue-detail")).toHaveAttribute(
      "data-issue-id",
      "issue-1",
    );
    expect(screen.getByTestId("issue-detail")).toHaveAttribute(
      "data-sidebar-open",
      "true",
    );
    expect(screen.getByTestId("issue-detail")).toHaveAttribute(
      "data-layout-id",
      "multica_issue_detail_drawer_layout_v2",
    );
  });
});
