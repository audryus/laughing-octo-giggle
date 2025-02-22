-- name: GetUserByUsername :one
select * from users 
where username = ? limit 1;

-- name: CreateUser :one
insert into users(
    username, password_hash
) values (
    ?, ?
) 
returning *;

-- name: CreatePlayer :one
insert into players (
    user_id, name, color
) values (
    ?, ?, ?
)
returning *;

-- name: GetPlayerByUserId :one
select * from players
where user_id = ? limit 1;

-- name: UpdatePlayerBestScore :exec
update players
set best_score = ?
where id = ?;

-- name: GetTopScores :many
select name, best_score
from players
order by best_score DESC
limit ?
OFFSET ?;

-- name: GetPlayerByName :one
select * from players
where name LIKE ? 
limit 1;

-- name: GetPlayerRank :one
select count(*) + 1 as "rank" from players 
where best_score >= (
    select best_score from players p2
    where p2.id = ?
)