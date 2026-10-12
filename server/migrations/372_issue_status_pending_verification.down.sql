DELETE FROM issue_status
WHERE is_system = TRUE
  AND key = 'pending_verification';

ALTER TABLE issue_status DROP CONSTRAINT IF EXISTS issue_status_category_check;

ALTER TABLE issue_status
    ADD CONSTRAINT issue_status_category_check
    CHECK (category IN ('backlog', 'todo', 'in_progress', 'in_review', 'done', 'blocked', 'cancelled'));
