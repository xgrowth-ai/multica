UPDATE issue_status
SET category = 'pending_verification', is_system = TRUE
WHERE key = 'pending_verification';
