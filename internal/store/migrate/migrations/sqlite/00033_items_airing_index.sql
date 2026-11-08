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
CREATE INDEX ix_items_airing ON items((CASE WHEN end_date - start_date <= 3600000000 THEN 0 WHEN end_date - start_date <= 7200000000 THEN 1 WHEN end_date - start_date <= 14400000000 THEN 2 WHEN end_date - start_date <= 28800000000 THEN 3 WHEN end_date - start_date <= 86400000000 THEN 4 WHEN end_date - start_date <= 172800000000 THEN 5 WHEN end_date - start_date <= 604800000000 THEN 6 ELSE 7 END), start_date) WHERE start_date IS NOT NULL;

-- +goose Down
DROP INDEX ix_items_airing;
