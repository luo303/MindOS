-- 0022_checkpoint_owner_not_null.down.sql
ALTER TABLE agent_checkpoints ALTER COLUMN owner_id DROP NOT NULL;
