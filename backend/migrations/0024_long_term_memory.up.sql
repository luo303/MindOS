ALTER TABLE messages ADD CONSTRAINT messages_owner_id_id_unique UNIQUE (owner_id, id);

CREATE TABLE memories (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    owner_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    key text NOT NULL CHECK (key IN ('answer_language', 'answer_detail', 'code_example_language')),
    value text NOT NULL,
    status text NOT NULL CHECK (status IN ('active', 'archived')),
    version integer NOT NULL CHECK (version >= 1),
    updated_at timestamptz NOT NULL,
    UNIQUE (owner_id, key),
    UNIQUE (owner_id, id)
);

CREATE TABLE memory_versions (
    owner_id bigint NOT NULL,
    memory_id bigint NOT NULL,
    version integer NOT NULL,
    value text NOT NULL,
    status text NOT NULL,
    source text NOT NULL CHECK (source IN ('manual', 'chat')),
    message_id bigint,
    conversation_id bigint,
    created_at timestamptz NOT NULL,
    PRIMARY KEY (memory_id, version),
    FOREIGN KEY (owner_id, memory_id) REFERENCES memories(owner_id, id) ON DELETE CASCADE,
    FOREIGN KEY (owner_id, message_id) REFERENCES messages(owner_id, id) ON DELETE SET NULL (message_id)
);
CREATE INDEX memory_versions_source_idx ON memory_versions (owner_id, message_id) WHERE message_id IS NOT NULL;

CREATE TABLE memory_candidates (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    owner_id bigint NOT NULL,
    message_id bigint NOT NULL,
    conversation_id bigint NOT NULL,
    key text NOT NULL CHECK (key IN ('answer_language', 'answer_detail', 'code_example_language')),
    value text NOT NULL,
    evidence text NOT NULL,
    base_version integer NOT NULL CHECK (base_version >= 0),
    status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'accepted', 'rejected')),
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (owner_id, message_id),
    FOREIGN KEY (owner_id, message_id) REFERENCES messages(owner_id, id) ON DELETE CASCADE
);
CREATE INDEX memory_candidates_pending_idx ON memory_candidates (owner_id, id DESC) WHERE status = 'pending';
CREATE INDEX memory_candidates_conversation_idx ON memory_candidates (owner_id, conversation_id, id DESC) WHERE status = 'pending';
CREATE UNIQUE INDEX memory_candidates_one_pending_value_idx
    ON memory_candidates (owner_id, key, value, base_version) WHERE status = 'pending';
