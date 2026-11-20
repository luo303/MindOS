-- 0022_checkpoint_owner_not_null.up.sql
ALTER TABLE agent_checkpoints ALTER COLUMN owner_id SET NOT NULL;
