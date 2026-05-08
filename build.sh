export HTTP_PROXY="http://172.17.224.1:7890"
export HTTPS_PROXY="http://172.17.224.1:7890"

git fetch
git pull
docker-compose build
docker-compose down
docker-compose up -d