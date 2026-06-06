-- Chart credentials are now a single basic-auth username/password pair: the chart
-- pull picks helm's mechanism (repo add vs registry login) from the application's
-- chart_source, so the credential no longer carries its own type. Drop the column
-- introduced in 20260603140000_chart_credentials.sql.

ALTER TABLE chart_credentials DROP COLUMN type;
