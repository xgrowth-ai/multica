-- Preserve the fork key while upstream contracts categories to four lifecycle
-- values. Temporarily treat the row as custom so upstream canonical-system
-- constraints accept it; the final fork migration restores its built-in flag.
UPDATE issue_status
SET category = 'in_progress', is_system = FALSE
WHERE key = 'pending_verification';
