import { create } from "zustand";
import { createJSONStorage, persist } from "zustand/middleware";
import { defaultStorage } from "../../platform/storage";

interface IssueDetailOpenStore {
  openInDrawer: boolean;
  drawerIssueId: string | null;
  setOpenInDrawer: (open: boolean) => void;
  openDrawer: (issueId: string) => void;
  closeDrawer: () => void;
}

/**
 * Personal issue-detail navigation preference plus the transient drawer target.
 * Only the preference is persisted; an open drawer never survives a reload.
 */
export const useIssueDetailOpenStore = create<IssueDetailOpenStore>()(
  persist(
    (set) => ({
      openInDrawer: true,
      drawerIssueId: null,
      setOpenInDrawer: (open) =>
        set({ openInDrawer: open, ...(open ? {} : { drawerIssueId: null }) }),
      openDrawer: (issueId) => set({ drawerIssueId: issueId }),
      closeDrawer: () => set({ drawerIssueId: null }),
    }),
    {
      name: "multica_issue_detail_open",
      storage: createJSONStorage(() => defaultStorage),
      partialize: (state) => ({ openInDrawer: state.openInDrawer }),
    },
  ),
);
