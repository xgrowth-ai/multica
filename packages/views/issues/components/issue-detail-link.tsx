"use client";

import { forwardRef } from "react";
import { useIssueDetailOpenStore } from "@multica/core/issues/stores";
import { AppLink } from "../../navigation";

interface IssueDetailLinkProps
  extends React.AnchorHTMLAttributes<HTMLAnchorElement> {
  href: string;
  issueId: string;
  newTabTitle?: string;
}

/** Link that routes plain clicks through the user's issue-detail preference. */
export const IssueDetailLink = forwardRef<
  HTMLAnchorElement,
  IssueDetailLinkProps
>(function IssueDetailLink(
  { issueId, onClick, target, ...props },
  ref,
) {
  const openInDrawer = useIssueDetailOpenStore((state) => state.openInDrawer);
  const openDrawer = useIssueDetailOpenStore((state) => state.openDrawer);

  return (
    <AppLink
      ref={ref}
      target={target}
      {...props}
      onClick={onClick}
      onNavigate={() => {
        if (!openInDrawer) return true;
        openDrawer(issueId);
        return false;
      }}
    />
  );
});
