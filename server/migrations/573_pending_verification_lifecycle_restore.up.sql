DO $migration$
BEGIN
    ALTER TABLE issue_status DROP CONSTRAINT IF EXISTS issue_status_system_is_canonical;
    ALTER TABLE issue_status ADD CONSTRAINT issue_status_system_is_canonical CHECK (
        NOT is_system OR (key, category) IN (
            ('backlog', 'unstarted'), ('todo', 'unstarted'),
            ('in_progress', 'started'), ('in_review', 'started'),
            ('pending_verification', 'started'), ('blocked', 'started'),
            ('done', 'done'), ('cancelled', 'closed')
        )
    );

    INSERT INTO issue_status (workspace_id, key, name, description, category, color, is_system, position)
    SELECT id, 'pending_verification', 'Pending Verification',
           'Delivered, waiting on product or operations verification.',
           'started', '#06b6d4', TRUE, 0
    FROM workspace
    WHERE TRUE
    ON CONFLICT (workspace_id, key) DO UPDATE
    SET category = 'started', is_system = TRUE;
END
$migration$;
