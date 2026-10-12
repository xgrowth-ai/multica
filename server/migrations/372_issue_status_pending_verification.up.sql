-- Preserve the fork's pending-verification workflow as the eighth built-in
-- category in the upstream issue-status catalog.
ALTER TABLE issue_status DROP CONSTRAINT IF EXISTS issue_status_category_check;

ALTER TABLE issue_status
    ADD CONSTRAINT issue_status_category_check
    CHECK (category IN ('backlog', 'todo', 'in_progress', 'in_review', 'pending_verification', 'done', 'blocked', 'cancelled'));

INSERT INTO issue_status (workspace_id, key, name, description, category, color, is_system, position)
SELECT w.id,
       'pending_verification',
       'Pending Verification',
       'Delivered, waiting on product or operations verification.',
       'pending_verification',
       '#06b6d4',
       TRUE,
       0
FROM workspace w
ON CONFLICT DO NOTHING;
