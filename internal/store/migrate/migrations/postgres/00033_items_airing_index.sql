-- +goose Up
-- An airing query (IsAiring, LiveTvManager.GetPrograms and
-- GetRecommendedProgramsInternal) matches start_date <= now AND
-- end_date >= now. ix_items_top_kind_start reads every programme that
-- started before now, past ones included (#892). This index keys the
-- programmes on a class of their span (store.AiringClass: at most 1 h,
-- 2 h, 4 h, 8 h, 1 day, 2 days, 7 days, or longer) and their start, so a
-- query reads in each class only the starts since now minus the class
-- limit. The expression must stay equal to store.AiringClass. Each arm of
-- the query bounds start_date, which implies the partial index predicate.
CREATE INDEX ix_items_airing ON items((CASE WHEN end_date - start_date <= interval '3600 seconds' THEN 0 WHEN end_date - start_date <= interval '7200 seconds' THEN 1 WHEN end_date - start_date <= interval '14400 seconds' THEN 2 WHEN end_date - start_date <= interval '28800 seconds' THEN 3 WHEN end_date - start_date <= interval '86400 seconds' THEN 4 WHEN end_date - start_date <= interval '172800 seconds' THEN 5 WHEN end_date - start_date <= interval '604800 seconds' THEN 6 ELSE 7 END), start_date) WHERE start_date IS NOT NULL;

-- +goose Down
DROP INDEX ix_items_airing;
