UPDATE users
SET quota = quota + (
    SELECT SUM(remain_quota)
    FROM tokens
    WHERE tokens.user_id = users.id
)
