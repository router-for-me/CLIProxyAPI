INSERT INTO abilities (`group`, model, channel_id, enabled)
SELECT c.`group`, m.model, c.id, 1
FROM channels c
CROSS JOIN (
    SELECT 'gpt-3.5-turbo' AS model UNION ALL
    SELECT 'gpt-3.5-turbo-0301' AS model UNION ALL
    SELECT 'gpt-4' AS model UNION ALL
    SELECT 'gpt-4-0314' AS model
) AS m
WHERE c.status = 1
  AND NOT EXISTS (
    SELECT 1
    FROM abilities a
    WHERE a.`group` = c.`group`
      AND a.model = m.model
      AND a.channel_id = c.id
);
