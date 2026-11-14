-- Every Run with a repository names its host (issue #413). A Run from before hosts existed (00020) has a NULL
-- repo_host, which meant github.com; it is now written down, so nothing reads NULL as github.com any more.

-- +goose Up
UPDATE runs SET repo_host = 'github.com' WHERE repo IS NOT NULL AND repo_host IS NULL;

-- +goose Down
-- Which Runs had no host before cannot be told apart from those created on github.com; they are left as they are.
SELECT 1;
