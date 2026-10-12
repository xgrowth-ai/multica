DO $migration$
BEGIN
    UPDATE issue_status SET is_system = FALSE WHERE key = 'pending_verification';
    ALTER TABLE issue_status DROP CONSTRAINT IF EXISTS issue_status_system_is_canonical;
    ALTER TABLE issue_status ADD CONSTRAINT issue_status_system_is_canonical CHECK (
        NOT is_system OR (key, category) IN (
            ('backlog', 'unstarted'), ('todo', 'unstarted'),
            ('in_progress', 'started'), ('in_review', 'started'),
            ('blocked', 'started'), ('done', 'done'), ('cancelled', 'closed')
        )
    );
END
$migration$;
