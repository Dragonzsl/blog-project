#!/bin/sh
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)

usage() {
	cat <<'EOF'
用法：./scripts/deploy.sh [选项]

首次运行按提示设置域名，使用 Docker Compose 构建、启动并检查博客。

选项：
  --env-file FILE    使用指定的 Compose 环境文件（默认：.env）
  --domain DOMAIN    配置域名并自动启用 HTTPS；localhost 用于本地体验
  --configure        重新提示配置域名（其他配置保持不变）
  --non-interactive  不提示输入，首次未指定域名时使用 localhost
  --no-build         不重新构建应用镜像
  --backup           要求升级前创建恢复点；没有运行中的 app 时失败
  --no-backup        跳过升级前恢复点
  --wait SECONDS     就绪检查最长等待时间（默认：180）
  -h, --help         显示帮助

也可以通过环境变量设置：
  DEPLOY_ENV_FILE、DEPLOY_DOMAIN、DEPLOY_BACKUP=auto|always|never、DEPLOY_WAIT_SECONDS
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
WAIT_SECONDS=${DEPLOY_WAIT_SECONDS:-180}
BACKUP_MODE=${DEPLOY_BACKUP:-auto}
BUILD=1
DOMAIN_INPUT=${DEPLOY_DOMAIN:-}
CONFIGURE=0
NON_INTERACTIVE=0
ENV_TEMP=
trap '[ -z "$ENV_TEMP" ] || rm -f "$ENV_TEMP"' EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

normalize_domain() {
	printf '%s\n' "$1" | awk '
		NR > 1 { exit 1 }
		{
			value = tolower($0)
			sub(/^[ \t]+/, "", value); sub(/[ \t]+$/, "", value)
			sub(/^https:\/\//, "", value); sub(/\/$/, "", value)
			if (value == "localhost") { print value; exit }
			if (length(value) > 253 || value !~ /\./ || value ~ /^[0-9.]+$/) exit 1
			count = split(value, labels, ".")
			for (i = 1; i <= count; i++) {
				if (length(labels[i]) < 1 || length(labels[i]) > 63 ||
					labels[i] !~ /^[a-z0-9-]+$/ || labels[i] ~ /^-/ || labels[i] ~ /-$/) exit 1
			}
			if (value ~ /\.(localhost|local|internal)$/ || value ~ /\.home\.arpa$/) exit 1
			print value
		}'
}

env_site_address() {
	# Read only a literal value: never execute/source an environment file.
	awk '/^[ \t]*BLOG_SITE_ADDRESS[ \t]*=/ {
		value = $0; sub(/^[^=]*=[ \t]*/, "", value); sub(/[ \t]+$/, "", value)
		gsub(/^["\047]|["\047]$/, "", value)
	} END { print value }' "$ENV_FILE"
}

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
		--domain)
			[ "$#" -ge 2 ] && [ -n "$2" ] || fail "--domain 需要域名"
			DOMAIN_INPUT=$2
			shift 2
			;;
		--domain=*)
			DOMAIN_INPUT=${1#*=}
			[ -n "$DOMAIN_INPUT" ] || fail "--domain 需要域名"
			shift
			;;
		--configure)
			CONFIGURE=1
			shift
			;;
		--non-interactive)
			NON_INTERACTIVE=1
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
docker info >/dev/null 2>&1 || fail "Docker 未运行或当前用户无访问权限；请启动 Docker 并确认 docker info 可用"

NEW_ENV=0
if [ ! -f "$ENV_FILE" ]; then
	[ "$ENV_FILE" = "$ROOT/.env" ] || fail "环境文件不存在：$ENV_FILE"
	[ -f "$ROOT/.env.example" ] || fail "缺少 .env.example，无法初始化环境文件"
	NEW_ENV=1
fi

if [ "$NEW_ENV" -eq 1 ] || [ "$CONFIGURE" -eq 1 ] || [ -n "$DOMAIN_INPUT" ]; then
	[ ! -L "$ENV_FILE" ] || fail "环境文件是符号链接，请指定普通文件"
	DEFAULT_DOMAIN=localhost
	if [ "$NEW_ENV" -eq 0 ]; then
		current_address=$(env_site_address)
		DEFAULT_DOMAIN=$(normalize_domain "$current_address" 2>/dev/null || printf localhost)
	fi
	if [ -z "$DOMAIN_INPUT" ]; then
		if [ "$NON_INTERACTIVE" -eq 0 ] && { [ -t 0 ] || [ "$CONFIGURE" -eq 1 ]; }; then
			log "请输入已解析到本机的域名。公网 HTTPS 需要开放 TCP 80、443；留空使用本地 localhost。"
			printf '站点域名 [%s]: ' "$DEFAULT_DOMAIN"
			while :; do
				IFS= read -r DOMAIN_INPUT || fail "未读取到输入；自动部署请使用 --domain 或 --non-interactive"
				DOMAIN_INPUT=${DOMAIN_INPUT:-$DEFAULT_DOMAIN}
				if DEPLOY_DOMAIN_VALUE=$(normalize_domain "$DOMAIN_INPUT"); then break; fi
				printf '域名无效。请输入域名或 https://域名（不含路径、端口、通配符），或 localhost: '
			done
		else
			[ "$CONFIGURE" -eq 0 ] || fail "非交互重新配置必须提供 --domain"
			DOMAIN_INPUT=$DEFAULT_DOMAIN
		fi
	fi
	DEPLOY_DOMAIN_VALUE=$(normalize_domain "$DOMAIN_INPUT") || fail "域名无效：请输入公网域名或 localhost，不含路径、端口、通配符"
	DEPLOY_ADDRESS="https://$DEPLOY_DOMAIN_VALUE"
	ENV_TEMP=$(mktemp "$ENV_FILE.tmp.XXXXXX")
	chmod 600 "$ENV_TEMP"
	if [ "$NEW_ENV" -eq 1 ]; then ENV_SOURCE="$ROOT/.env.example"; else ENV_SOURCE=$ENV_FILE; fi
	awk -v address="$DEPLOY_ADDRESS" '
		/^[ \t]*BLOG_SITE_ADDRESS[ \t]*=/ {
			if (!written) print "BLOG_SITE_ADDRESS=" address
			written = 1; next
		}
		{ print }
		END { if (!written) print "BLOG_SITE_ADDRESS=" address }
	' "$ENV_SOURCE" > "$ENV_TEMP"
	mv "$ENV_TEMP" "$ENV_FILE"
	ENV_TEMP=
	# Shell variables have precedence over dotenv in Compose.
	export BLOG_SITE_ADDRESS="$DEPLOY_ADDRESS"
	log "已保存站点地址：${DEPLOY_ADDRESS}（配置文件权限 0600）"
fi

if [ "$NEW_ENV" -eq 0 ] && [ "$CONFIGURE" -eq 0 ] && [ -z "$DOMAIN_INPUT" ]; then
	log "沿用现有配置；修改域名可使用 --configure 或 --domain"
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
	log "请检查域名 A/AAAA 记录、80/443 端口、防火墙与 Caddy 证书日志；修正后重新运行脚本"
	exit 1
fi

compose ps
log "部署完成：${public_address%/}"
log "首次初始化：${public_address%/}/admin/setup"
log "管理后台：${public_address%/}/admin/login"
case "$public_address" in
	https://localhost|https://localhost:*) log "本地证书由 Caddy 签发；浏览器需信任本地 CA 才能消除证书提示" ;;
	*) log "HTTPS 证书由 Caddy 自动申请和续期；更换已有站点域名时请同步更新后台站点设置" ;;
esac
