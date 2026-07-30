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
        className="gap-0 overflow-hidden p-0 data-[side=right]:w-[min(2800px,calc(100vw-1.5rem))] data-[side=right]:sm:max-w-none"
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
            defaultSidebarOpen
            layoutId="multica_issue_detail_drawer_layout_v2"
          />
        )}
      </SheetContent>
    </Sheet>
  );
}
