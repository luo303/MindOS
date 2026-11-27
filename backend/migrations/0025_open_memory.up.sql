ALTER TABLE memories DROP CONSTRAINT memories_key_check;
ALTER TABLE memory_candidates DROP CONSTRAINT memory_candidates_key_check;
ALTER TABLE memory_candidates DROP CONSTRAINT memory_candidates_owner_id_message_id_key;
ALTER TABLE memories ADD COLUMN category text NOT NULL DEFAULT 'preference',
    ADD COLUMN scope text NOT NULL DEFAULT '', ADD COLUMN expires_at timestamptz,
    ADD COLUMN pinned boolean NOT NULL DEFAULT false;
ALTER TABLE memory_versions ADD COLUMN category text NOT NULL DEFAULT 'preference',
    ADD COLUMN scope text NOT NULL DEFAULT '', ADD COLUMN expires_at timestamptz,
    ADD COLUMN pinned boolean NOT NULL DEFAULT false,
    ADD COLUMN automatic boolean NOT NULL DEFAULT false;
ALTER TABLE memory_candidates ADD COLUMN category text NOT NULL DEFAULT 'preference',
    ADD COLUMN scope text NOT NULL DEFAULT '', ADD COLUMN expires_at timestamptz,
    ADD COLUMN pinned boolean NOT NULL DEFAULT false,
    ADD COLUMN target_id bigint,
    ADD COLUMN action text NOT NULL DEFAULT 'create' CHECK (action IN ('create', 'update', 'reinforce')),
    ADD CONSTRAINT memory_candidate_target_fk FOREIGN KEY (owner_id, target_id) REFERENCES memories(owner_id,id) ON DELETE CASCADE,
    ADD CONSTRAINT memory_candidates_source_key_unique UNIQUE (owner_id, message_id, key);
UPDATE memories SET pinned = true;
UPDATE memory_versions SET pinned = true;
UPDATE memory_candidates SET pinned = true;
UPDATE memory_candidates c SET action = 'update', target_id = m.id
FROM memories m WHERE c.owner_id = m.owner_id AND c.key = m.key AND c.base_version > 0;
CREATE INDEX memories_active_idx ON memories(owner_id, updated_at DESC) WHERE status = 'active';

CREATE TABLE memory_evidence (
    owner_id bigint NOT NULL,
    memory_id bigint NOT NULL,
    version integer NOT NULL,
    message_id bigint NOT NULL,
    conversation_id bigint NOT NULL,
    excerpt text NOT NULL,
    PRIMARY KEY (memory_id, version, message_id),
    FOREIGN KEY (owner_id, memory_id) REFERENCES memories(owner_id,id) ON DELETE CASCADE,
    FOREIGN KEY (memory_id, version) REFERENCES memory_versions(memory_id,version) ON DELETE CASCADE,
    FOREIGN KEY (owner_id, message_id) REFERENCES messages(owner_id,id) ON DELETE CASCADE
);
CREATE INDEX memory_evidence_source_idx ON memory_evidence(owner_id,message_id);
INSERT INTO memory_evidence(owner_id,memory_id,version,message_id,conversation_id,excerpt)
SELECT v.owner_id,v.memory_id,v.version,v.message_id,v.conversation_id,COALESCE(c.evidence,'')
FROM memory_versions v LEFT JOIN memory_candidates c ON c.owner_id=v.owner_id AND c.message_id=v.message_id AND c.key=(SELECT key FROM memories WHERE id=v.memory_id)
WHERE v.message_id IS NOT NULL AND v.conversation_id IS NOT NULL;

CREATE TABLE memory_extractions (
    owner_id bigint NOT NULL,
    message_id bigint NOT NULL,
    PRIMARY KEY (owner_id,message_id),
    FOREIGN KEY (owner_id,message_id) REFERENCES messages(owner_id,id) ON DELETE CASCADE
);
INSERT INTO memory_extractions SELECT DISTINCT owner_id,message_id FROM memory_candidates;
CREATE TABLE memory_settings (
    owner_id bigint PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    mode text NOT NULL DEFAULT 'auto' CHECK (mode IN ('auto','suggest','off'))
);
ALTER TABLE conversations ADD CONSTRAINT conversations_owner_id_id_unique UNIQUE(owner_id,id);
CREATE TABLE memory_conversation_settings (
    owner_id bigint NOT NULL,
    conversation_id bigint NOT NULL,
    PRIMARY KEY (owner_id,conversation_id),
    FOREIGN KEY (owner_id,conversation_id) REFERENCES conversations(owner_id,id) ON DELETE CASCADE
);
