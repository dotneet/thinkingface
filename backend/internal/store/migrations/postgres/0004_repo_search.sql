-- Full text search and facet filtering for repository listings.
--
-- README bodies are never stored in Postgres (they live in git / GCS), so the
-- search surface here is limited to what is already indexed: the repository
-- name plus the parsed README front matter (`repositories.card`, see
-- internal/repocard). That covers name, description/summary, license, tags,
-- and the model/dataset task fields -- everything the /datasets and /models
-- listing pages show today.
--
-- The tsvector is trigger-maintained rather than a generated column because
-- flattening `card->'tags'` (a jsonb array) into lexemes needs
-- jsonb_array_elements_text(), and Postgres generated column expressions may
-- not contain subqueries.

-- repo_card_text extracts a card field as searchable text regardless of
-- whether the author wrote it as a single string (`license: mit`) or a list
-- (`task_categories: [text-classification, summarization]`).
CREATE OR REPLACE FUNCTION repo_card_text(card JSONB, key TEXT) RETURNS TEXT AS $$
    SELECT CASE jsonb_typeof(card -> key)
        WHEN 'array' THEN (
            SELECT COALESCE(string_agg(v, ' '), '') FROM jsonb_array_elements_text(card -> key) AS v
        )
        WHEN 'string' THEN card ->> key
        ELSE ''
    END;
$$ LANGUAGE sql IMMUTABLE;

ALTER TABLE repositories ADD COLUMN IF NOT EXISTS search_vector TSVECTOR NOT NULL DEFAULT ''::tsvector;

CREATE OR REPLACE FUNCTION repositories_search_vector_trigger() RETURNS TRIGGER AS $$
BEGIN
    NEW.search_vector :=
        setweight(to_tsvector('simple', coalesce(NEW.name, '')), 'A') ||
        setweight(to_tsvector('simple', repo_card_text(NEW.card, 'tags')), 'A') ||
        setweight(to_tsvector('simple', coalesce(NEW.description, '')), 'B') ||
        setweight(to_tsvector('simple', coalesce(NEW.card ->> 'description', '')), 'B') ||
        setweight(to_tsvector('simple', coalesce(NEW.card ->> 'short_description', '')), 'B') ||
        setweight(to_tsvector('simple', coalesce(NEW.card ->> 'summary', '')), 'B') ||
        setweight(to_tsvector('simple', coalesce(NEW.card ->> 'license', '')), 'C') ||
        setweight(to_tsvector('simple', repo_card_text(NEW.card, 'pipeline_tag')), 'C') ||
        setweight(to_tsvector('simple', repo_card_text(NEW.card, 'task_categories')), 'C');
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_repositories_search_vector ON repositories;
CREATE TRIGGER trg_repositories_search_vector
    BEFORE INSERT OR UPDATE ON repositories
    FOR EACH ROW EXECUTE FUNCTION repositories_search_vector_trigger();

-- Backfill rows that existed before the trigger was created. `card = card` is
-- a no-op assignment: it changes nothing but still fires the BEFORE UPDATE
-- trigger for every row, which is all that is needed here.
UPDATE repositories SET card = card;

CREATE INDEX IF NOT EXISTS idx_repositories_search_vector ON repositories USING GIN (search_vector);

-- Tag containment (`card->'tags' @> '["a","b"]'`) and license equality are
-- the two facet filters the listing pages apply most often; index both.
CREATE INDEX IF NOT EXISTS idx_repositories_card_tags ON repositories USING GIN ((card -> 'tags'));
CREATE INDEX IF NOT EXISTS idx_repositories_card_license ON repositories ((card ->> 'license'));
