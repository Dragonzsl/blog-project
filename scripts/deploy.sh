#!/bin/sh
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)

usage() {
	cat <<'EOF'
用法：./scripts/deploy.sh [选项]

使用 Docker Compose 构建、启动并检查当前博客部署。

选项：
  --env-file FILE    使用指定的 Compose 环境文件（默认：.env）
  --no-build         不重新构建应用镜像
  --backup           要求升级前创建恢复点；没有运行中的 app 时失败
  --no-backup        跳过升级前恢复点
  --wait SECONDS     就绪检查最长等待时间（默认：90）
  -h, --help         显示帮助

也可以通过环境变量设置：
  DEPLOY_ENV_FILE、DEPLOY_BACKUP=auto|always|never、DEPLOY_WAIT_SECONDS
EOF
}

log() {
	printf '[deploy] %s\n' "$*"
}

fail() {
	printf '[deploy] 错误：%s\n' "$*" >&2
	exit 1
}

is_positive_integer() {
	case "$1" in
		''|*[!0-9]*) return 1 ;;
		*) [ "$1" -gt 0 ] ;;
	esac
}

ENV_FILE=${DEPLOY_ENV_FILE:-"$ROOT/.env"}
WAIT_SECONDS=${DEPLOY_WAIT_SECONDS:-90}
BACKUP_MODE=${DEPLOY_BACKUP:-auto}
BUILD=1

while [ "$#" -gt 0 ]; do
	case "$1" in
		--env-file)
			[ "$#" -ge 2 ] || fail "--env-file 需要一个文件路径"
			ENV_FILE=$2
			shift 2
			;;
		--env-file=*)
			ENV_FILE=${1#*=}
			shift
			;;
		--no-build)
			BUILD=0
			shift
			;;
		--backup)
			BACKUP_MODE=always
			shift
			;;
		--no-backup)
			BACKUP_MODE=never
			shift
			;;
		--wait)
			[ "$#" -ge 2 ] || fail "--wait 需要秒数"
			WAIT_SECONDS=$2
			shift 2
			;;
		--wait=*)
			WAIT_SECONDS=${1#*=}
			shift
			;;
		-h|--help)
			usage
			exit 0
			;;
		*)
			fail "未知选项：$1（使用 --help 查看帮助）"
			;;
	esac
done

case "$ENV_FILE" in
	/*) ;;
	*) ENV_FILE=$ROOT/$ENV_FILE ;;
esac

is_positive_integer "$WAIT_SECONDS" || fail "等待时间必须是正整数：$WAIT_SECONDS"
case "$BACKUP_MODE" in
	auto|always|never) ;;
	*) fail "DEPLOY_BACKUP 必须是 auto、always 或 never：$BACKUP_MODE" ;;
esac

cd "$ROOT"

command -v docker >/dev/null 2>&1 || fail "未找到 Docker，请先安装 Docker Engine/Desktop"
command -v curl >/dev/null 2>&1 || fail "未找到 curl，无法验证公开入口"
docker compose version >/dev/null 2>&1 || fail "未找到 Docker Compose v2，请确认 docker compose 可用"

if [ ! -f "$ENV_FILE" ]; then
	[ "$ENV_FILE" = "$ROOT/.env" ] || fail "环境文件不存在：$ENV_FILE"
	[ -f "$ROOT/.env.example" ] || fail "缺少 .env.example，无法初始化环境文件"
	cp "$ROOT/.env.example" "$ENV_FILE"
	chmod 600 "$ENV_FILE" 2>/dev/null || true
	log "已创建 $ENV_FILE；正式域名部署前请确认 BLOG_SITE_ADDRESS 等配置"
fi

compose() {
	docker compose --env-file "$ENV_FILE" -f "$ROOT/compose.yaml" "$@"
}

log "校验 Docker Compose 配置"
compose config --quiet

running_services=$(compose ps --services --filter status=running 2>/dev/null || true)
has_running_app=0
for service in $running_services; do
	if [ "$service" = app ]; then
		has_running_app=1
		break
	fi
done

if [ "$BACKUP_MODE" = always ] && [ "$has_running_app" -ne 1 ]; then
	fail "未检测到运行中的 app，无法创建升级前恢复点"
fi

if [ "$BACKUP_MODE" != never ] && [ "$has_running_app" -eq 1 ]; then
	log "创建升级前恢复点"
	compose exec -T app /blog upgrade prepare
elif [ "$BACKUP_MODE" = auto ]; then
	log "首次部署或 app 未运行，跳过升级前恢复点"
fi

if [ "$BUILD" -eq 1 ]; then
	log "构建并启动服务"
	if ! compose up --build -d; then
		log "启动失败，输出服务状态和最近日志"
		compose ps || true
		compose logs --tail=100 app caddy || true
		exit 1
	fi
else
	log "启动服务（跳过构建）"
	if ! compose up -d; then
		log "启动失败，输出服务状态和最近日志"
		compose ps || true
		compose logs --tail=100 app caddy || true
		exit 1
	fi
fi

deadline=$(($(date +%s) + WAIT_SECONDS))
ready=0
log "等待 app 和公开入口就绪（最长 ${WAIT_SECONDS}s）"
while [ "$(date +%s)" -lt "$deadline" ]; do
	if compose exec -T app /blog healthcheck --url http://127.0.0.1:8080/readyz >/dev/null 2>&1; then
		public_address=$(compose exec -T caddy printenv BLOG_SITE_ADDRESS 2>/dev/null || true)
		case "$public_address" in
			http://*|https://*) ;;
			*) sleep 2; continue ;;
		esac
		# Caddy's local development certificate is not in the system trust store.
		# Production domains must pass normal TLS certificate verification.
		set --
		case "$public_address" in
			https://localhost|https://localhost:*) set -- --insecure ;;
		esac
		public_ready=1
		for endpoint in /readyz /; do
			status=$(curl "$@" --proto '=http,https' --silent --show-error \
				--connect-timeout 2 --max-time 3 --output /dev/null \
				--write-out '%{http_code}' "${public_address%/}$endpoint" 2>/dev/null || true)
			if [ "$status" != 200 ]; then
				public_ready=0
				break
			fi
		done
		if [ "$public_ready" -eq 1 ]; then
			ready=1
			break
		fi
	fi
	sleep 2
done

if [ "$ready" -ne 1 ]; then
	log "app 或公开入口未在期限内就绪，输出服务状态和最近日志"
	compose ps || true
	compose logs --tail=100 app caddy || true
	exit 1
fi

compose ps
log "部署完成；请访问 BLOG_SITE_ADDRESS 配置的地址（首次使用请打开 /admin/setup）"
