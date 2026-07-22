"use client";

import { useEffect, useRef } from "react";
import { useIssueDetailOpenStore } from "@multica/core/issues/stores";
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetTitle,
} from "@multica/ui/components/ui/sheet";
import { useT } from "../../i18n";
import { useNavigation } from "../../navigation";
import { IssueDetail } from "./issue-detail";

export function IssueDetailDrawer() {
  const { t } = useT("issues");
  const issueId = useIssueDetailOpenStore((state) => state.drawerIssueId);
  const closeDrawer = useIssueDetailOpenStore((state) => state.closeDrawer);
  const { pathname } = useNavigation();
  const previousPathname = useRef(pathname);

  useEffect(() => {
    if (previousPathname.current !== pathname) closeDrawer();
    previousPathname.current = pathname;
  }, [closeDrawer, pathname]);

  return (
    <Sheet
      open={issueId !== null}
      onOpenChange={(open) => {
        if (!open) closeDrawer();
      }}
    >
      <SheetContent
        side="right"
        className="w-[min(1100px,calc(100vw-1rem))] max-w-none gap-0 overflow-hidden p-0 sm:max-w-none"
      >
        <SheetTitle className="sr-only">
          {t(($) => $.detail_drawer.title)}
        </SheetTitle>
        <SheetDescription className="sr-only">
          {t(($) => $.detail_drawer.description)}
        </SheetDescription>
        {issueId && (
          <IssueDetail
            key={issueId}
            issueId={issueId}
            onDelete={closeDrawer}
            defaultSidebarOpen={false}
            layoutId="multica_issue_detail_drawer_layout"
          />
        )}
      </SheetContent>
    </Sheet>
  );
}
