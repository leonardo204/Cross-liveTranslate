/**
 * Cross-liveTranslate — 랜딩 + 개인정보처리방침 + 다운로드 리다이렉트 Worker
 *
 * 라우트:
 *   /                  랜딩 페이지
 *   /privacy           개인정보처리방침
 *   /download/mac      최신 macOS DMG 로 302
 *   /download/win      최신 Windows 설치 파일로 302
 *   /download/win-portable  최신 Windows 포터블 exe 로 302
 *   /robots.txt        AI 검색 크롤러 명시 허용 + sitemap
 *   /sitemap.xml
 *   그 외              정적 에셋(ASSETS: /assets/*)
 *
 * 도메인: live-translate.zerolive.co.kr
 *
 * 다운로드 주소는 릴리스마다 파일명에 버전이 박히므로 고정 링크가 없다.
 * 대신 파일명이 고정인 latest.json 을 읽어 실제 자산 주소를 얻는다.
 */

const SITE = "https://live-translate.zerolive.co.kr";
const REPO = "https://github.com/leonardo204/Cross-liveTranslate";
const RELEASES = REPO + "/releases";
const LATEST_JSON = RELEASES + "/latest/download/latest.json";
const CONTACT_EMAIL = "zerolive7@gmail.com";

/** latest.json 을 읽지 못했을 때 페이지에 표시할 버전. 릴리스 때 함께 올린다. */
const FALLBACK_VERSION = "1.6.0";

interface Env {
	ASSETS: Fetcher;
}

interface Manifest {
	version: string;
	platforms: Record<string, { url: string }>;
}

/** latest.json 조회. 실패하면 null. 엣지 캐시 10분. */
async function loadManifest(): Promise<Manifest | null> {
	try {
		const resp = await fetch(LATEST_JSON, {
			cf: { cacheTtl: 600, cacheEverything: true },
			headers: { "User-Agent": "live-translate-landing" },
		});
		if (!resp.ok) return null;
		const data = (await resp.json()) as Manifest;
		if (!data || typeof data.version !== "string" || !data.platforms) return null;
		return data;
	} catch {
		return null;
	}
}

/**
 * 플랫폼별 다운로드 주소.
 * Windows 설치 파일은 latest.json 에 없다(자동 업데이트는 포터블 exe 를 쓴다).
 * 포터블 주소의 파일명 규칙에서 설치 파일 이름을 만든다.
 */
function assetURL(m: Manifest | null, kind: "mac" | "win" | "win-portable"): string {
	if (!m) return RELEASES + "/latest";
	const mac = m.platforms["darwin-aarch64"]?.url || m.platforms["darwin-x86_64"]?.url;
	const win = m.platforms["windows-x86_64"]?.url;
	if (kind === "mac") return mac || RELEASES + "/latest";
	if (kind === "win-portable") return win || RELEASES + "/latest";
	if (!win) return RELEASES + "/latest";
	return win.replace(/_windows_amd64\.exe$/, "_windows_amd64_installer.exe");
}

async function route(request: Request, env: Env): Promise<Response> {
	const url = new URL(request.url);
	const path = url.pathname;

	if (path === "/robots.txt") {
		return new Response(ROBOTS, {
			headers: { "Content-Type": "text/plain;charset=UTF-8", "Cache-Control": "public, max-age=3600" },
		});
	}

	if (path === "/sitemap.xml") {
		return new Response(SITEMAP, {
			headers: { "Content-Type": "application/xml;charset=UTF-8", "Cache-Control": "public, max-age=3600" },
		});
	}

	if (path === "/download/mac" || path === "/download/win" || path === "/download/win-portable") {
		const kind = path.slice("/download/".length) as "mac" | "win" | "win-portable";
		const m = await loadManifest();
		return Response.redirect(assetURL(m, kind), 302);
	}

	if (path === "/" || path === "/index.html") {
		const m = await loadManifest();
		return html(renderLanding(m?.version || FALLBACK_VERSION));
	}

	if (path === "/privacy" || path === "/privacy/" || path === "/privacy.html") {
		return html(renderPrivacy());
	}

	return env.ASSETS.fetch(request);
}

/**
 * 평문 HTTP 차단 + HSTS.
 * Cloudflare 뒤에서는 원 요청 스킴이 CF-Visitor 헤더에 담긴다(없으면 URL 스킴으로 판단).
 * HSTS 는 includeSubDomains 없이 이 호스트에만 건다(zerolive.co.kr 의 다른 서브도메인 보호).
 */
function isInsecure(request: Request, url: URL): boolean {
	const cfv = request.headers.get("CF-Visitor");
	if (cfv) {
		try {
			const scheme = (JSON.parse(cfv) as { scheme?: string }).scheme;
			if (scheme) return scheme !== "https";
		} catch {
			/* 형식이 바뀌면 아래 URL 스킴으로 판단 */
		}
	}
	return url.protocol === "http:";
}

export default {
	async fetch(request: Request, env: Env): Promise<Response> {
		const url = new URL(request.url);
		if (isInsecure(request, url)) {
			url.protocol = "https:";
			return Response.redirect(url.toString(), 301);
		}
		const resp = await route(request, env);
		const out = new Response(resp.body, resp);
		out.headers.set("Strict-Transport-Security", "max-age=31536000");
		out.headers.set("X-Content-Type-Options", "nosniff");
		out.headers.set("Referrer-Policy", "strict-origin-when-cross-origin");
		return out;
	},
} satisfies ExportedHandler<Env>;

function html(body: string, status = 200): Response {
	return new Response(body, {
		status,
		headers: {
			"Content-Type": "text/html;charset=UTF-8",
			"Cache-Control": "public, max-age=300",
		},
	});
}

// ─────────────────────────────────────────────────────────────
// robots.txt — AI 검색 크롤러를 명시적으로 허용한다.
//
// 검색·인용용 봇과 학습용 봇은 별개 토큰이다. OpenAI 문서는 OAI-SearchBot 을 막으면
// ChatGPT 검색 답변에 사이트가 나오지 않는다고 명시한다. 기본값(전체 허용)만으로도
// 충분하지만, 나중에 실수로 막는 일을 줄이려고 이름을 적어 둔다.
// ─────────────────────────────────────────────────────────────
const ROBOTS = `User-agent: *
Allow: /

# AI 검색·인용
User-agent: OAI-SearchBot
Allow: /
User-agent: ChatGPT-User
Allow: /
User-agent: Claude-SearchBot
Allow: /
User-agent: Claude-User
Allow: /
User-agent: PerplexityBot
Allow: /
User-agent: Perplexity-User
Allow: /
User-agent: Google-Extended
Allow: /

# AI 학습
User-agent: GPTBot
Allow: /
User-agent: ClaudeBot
Allow: /

Sitemap: ${SITE}/sitemap.xml
`;

const SITEMAP = `<?xml version="1.0" encoding="UTF-8"?>
<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">
  <url><loc>${SITE}/</loc><changefreq>weekly</changefreq><priority>1.0</priority></url>
  <url><loc>${SITE}/privacy</loc><changefreq>yearly</changefreq><priority>0.3</priority></url>
</urlset>
`;

// ─────────────────────────────────────────────────────────────
// 공통 스타일 (라이트 미니멀, 이모지 미사용)
// ─────────────────────────────────────────────────────────────
const BASE_CSS = `
:root{
  --bg:#F6F8FB; --panel:#ffffff; --ink:#111823; --muted:#5A6675; --faint:#8A94A3;
  --line:#E3E9F1; --navy:#1E3A5F; --blue:#3D7BD9; --blue-soft:#EAF1FC;
  --amber:#B47500; --green:#0F7B4F; --red:#C0392B;
  --radius:18px; --radius-sm:12px;
  --shadow:0 1px 3px rgba(17,24,35,.05), 0 8px 28px rgba(17,24,35,.05);
  --shadow-lg:0 18px 50px rgba(30,58,95,.14);
  --font:-apple-system,BlinkMacSystemFont,"SF Pro Text","Apple SD Gothic Neo","Malgun Gothic","Segoe UI",Inter,sans-serif;
  --mono:ui-monospace,SFMono-Regular,"SF Mono",Menlo,Consolas,monospace;
}
*{box-sizing:border-box;margin:0;padding:0;-webkit-font-smoothing:antialiased}
html{scroll-behavior:smooth}
body{background:var(--bg);color:var(--ink);font-family:var(--font);line-height:1.65;word-break:keep-all;overflow-x:hidden}
a{color:var(--blue);text-decoration:none}
a:hover{text-decoration:underline}
.wrap{max-width:1000px;margin:0 auto;padding:0 22px}
section{padding:76px 0}
h2{font-size:clamp(22px,3.6vw,30px);font-weight:800;letter-spacing:-.6px;line-height:1.35;margin-bottom:12px}
h3{font-size:17px;font-weight:700;letter-spacing:-.2px}
.lead{font-size:16px;color:var(--muted);max-width:620px}
.eyebrow{display:inline-block;font-size:12px;font-weight:800;letter-spacing:.6px;color:var(--blue);
  background:var(--blue-soft);border-radius:999px;padding:5px 13px;margin-bottom:14px}

/* 헤더 */
.site-head{position:sticky;top:0;z-index:20;background:rgba(246,248,251,.88);
  backdrop-filter:blur(12px);-webkit-backdrop-filter:blur(12px);border-bottom:1px solid var(--line)}
.site-head .wrap{display:flex;align-items:center;justify-content:space-between;height:62px}
.brand{display:flex;align-items:center;gap:10px;font-weight:800;font-size:16px;letter-spacing:-.3px;color:var(--ink)}
.brand img{width:30px;height:30px;border-radius:8px}
.head-links{display:flex;align-items:center;gap:18px;font-size:14px;font-weight:600}
.head-links a{color:var(--muted)}
.head-links a:hover{color:var(--ink);text-decoration:none}
.head-cta{background:var(--navy);color:#fff!important;padding:8px 16px;border-radius:10px;font-size:13px}
.head-cta:hover{background:#16304F;text-decoration:none}
@media(max-width:640px){.head-links .hide-sm{display:none}}

/* 푸터 */
footer{border-top:1px solid var(--line);background:var(--panel);padding:34px 0 46px;
  color:var(--muted);font-size:13px}
footer .wrap{display:flex;flex-wrap:wrap;gap:12px 22px;align-items:center;justify-content:space-between}
footer a{color:var(--muted);font-weight:600}
`;

function shell(opts: {
	title: string;
	desc: string;
	path: string;
	css?: string;
	jsonld?: string[];
	body: string;
}): string {
	const canonical = SITE + opts.path;
	const ld = (opts.jsonld || [])
		.map((j) => `<script type="application/ld+json">${j}</script>`)
		.join("\n");
	return `<!DOCTYPE html><html lang="ko"><head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>${opts.title}</title>
<meta name="description" content="${opts.desc}">
<link rel="canonical" href="${canonical}">
<meta name="robots" content="index,follow,max-image-preview:large,max-snippet:-1">
<meta name="author" content="zerolive.co.kr">
<meta property="og:site_name" content="Cross-liveTranslate">
<meta property="og:locale" content="ko_KR">
<meta property="og:type" content="website">
<meta property="og:title" content="${opts.title}">
<meta property="og:description" content="${opts.desc}">
<meta property="og:url" content="${canonical}">
<meta property="og:image" content="${SITE}/assets/icon.png">
<meta property="og:image:width" content="1024">
<meta property="og:image:height" content="1024">
<meta property="og:image:alt" content="Cross-liveTranslate 앱 아이콘">
<meta name="twitter:card" content="summary_large_image">
<meta name="twitter:title" content="${opts.title}">
<meta name="twitter:description" content="${opts.desc}">
<meta name="twitter:image" content="${SITE}/assets/icon.png">
<link rel="icon" href="${SITE}/assets/icon.png">
<link rel="apple-touch-icon" href="${SITE}/assets/icon.png">
<style>${BASE_CSS}${opts.css || ""}</style>
${ld}
</head><body>
<header class="site-head"><div class="wrap">
  <a class="brand" href="/"><img src="/assets/icon.png" alt="Cross-liveTranslate 아이콘">Cross-liveTranslate</a>
  <nav class="head-links">
    <a class="hide-sm" href="/#features">기능</a>
    <a class="hide-sm" href="/#cost">비용</a>
    <a class="hide-sm" href="/#faq">자주 묻는 질문</a>
    <a class="head-cta" href="/#download">내려받기</a>
  </nav>
</div></header>
${opts.body}
<footer><div class="wrap">
  <span>© 2026 zerolive.co.kr · 개인 개발 프로젝트</span>
  <span>
    <a href="${REPO}" rel="noopener">GitHub</a> &nbsp;·&nbsp;
    <a href="/privacy">개인정보처리방침</a> &nbsp;·&nbsp;
    <a href="mailto:${CONTACT_EMAIL}">문의</a>
  </span>
</div></footer>
</body></html>`;
}

// ─────────────────────────────────────────────────────────────
// 자주 묻는 질문 — 본문과 JSON-LD 가 같은 데이터를 쓴다(어긋나지 않게).
// ─────────────────────────────────────────────────────────────
const FAQ: Array<{ q: string; a: string }> = [
	{
		q: "맥과 윈도우에서 모두 쓸 수 있나요?",
		a: "네. macOS 12 이상, Windows 10·11 64비트에서 같은 기능으로 동작합니다. 자막 오버레이, 번역 음성 재생, 자막 녹화, 자동 업데이트가 두 운영체제에 모두 들어 있습니다.",
	},
	{
		q: "줌이나 팀즈 회의 소리도 자막으로 번역되나요?",
		a: "네. 컴퓨터 스피커로 나오는 소리라면 프로그램을 가리지 않습니다. Zoom, Microsoft Teams, Google Meet, 유튜브, 온라인 강의 영상 모두 같은 방식으로 잡힙니다.",
	},
	{
		q: "가상 오디오 장치를 따로 설치해야 하나요?",
		a: "아니요. macOS에서는 Core Audio Process Tap, Windows에서는 WASAPI 루프백으로 시스템 소리를 바로 받습니다. 기본 출력 장치를 바꿀 필요도, 화면 녹화 권한을 줄 필요도 없습니다.",
	},
	{
		q: "무료인가요? 번역 비용은 누가 내나요?",
		a: "앱은 무료이고 회원가입도 없습니다. 다만 번역에 Google Gemini API를 쓰기 때문에, 직접 발급받은 API 키로 사용 요금이 나갑니다. 앱 화면에 이번 사용분과 누적 금액이 표시됩니다.",
	},
	{
		q: "번역 요금은 얼마나 나오나요?",
		a: "Gemini 3.5 Live Translate 기준으로 오디오 입력은 100만 토큰당 3.50달러, 오디오 출력은 100만 토큰당 21.00달러입니다. 출력 오디오는 재생하지 않아도 만들어지고 요금이 붙습니다. 말이 없는 구간은 보내지 않는 기능이 기본으로 켜져 있어 조용한 시간의 요금을 줄입니다.",
	},
	{
		q: "인터넷 없이도 쓸 수 있나요?",
		a: "쓸 수 없습니다. 번역이 Google Gemini API를 거치기 때문에 인터넷 연결이 필요합니다.",
	},
	{
		q: "소리가 어디로 전송되나요? 저장되나요?",
		a: "소리는 번역을 위해 Google Gemini API로 전송됩니다. 이 앱은 별도의 서버를 두지 않으며, 만든 사람에게 소리나 자막이 전달되지 않습니다. 자막 녹화를 켰을 때만 지정한 폴더에 텍스트 파일이 저장되고, 그 파일은 어디로도 올라가지 않습니다.",
	},
	{
		q: "몇 개 언어를 지원하나요?",
		a: "Gemini 3.5 Live Translate 모델이 70개가 넘는 언어를 자동으로 알아듣습니다. 번역해서 보여줄 언어는 설정에서 고르며 기본값은 한국어입니다.",
	},
	{
		q: "마이크로 들어오는 대화도 번역되나요?",
		a: "네. 입력을 마이크로 바꾸면 눈앞에서 오가는 대화도 자막으로 볼 수 있습니다. 자동으로 두면 상황에 맞는 입력을 골라 줍니다.",
	},
	{
		q: "자막을 파일로 남길 수 있나요?",
		a: "네. 녹화를 켜면 확정된 자막이 [시각] 원문 → 번역문 형식의 텍스트 파일로 저장됩니다. 저장 폴더는 설정에서 바꿉니다.",
	},
	{
		q: "윈도우에서 설치 파일과 포터블 중 무엇을 받아야 하나요?",
		a: "처음 쓴다면 설치 파일을 받으세요. 시작 메뉴 바로가기와 화면 표시에 필요한 런타임까지 함께 설치됩니다. 설치 없이 폴더에 두고 쓰려면 포터블 파일을 받으세요.",
	},
	{
		q: "업데이트는 어떻게 하나요?",
		a: "앱이 새 버전을 스스로 확인하고 알려 줍니다. 내려받은 파일은 서명을 검증한 뒤에만 설치되며, 설치가 끝나면 앱이 다시 실행됩니다.",
	},
];

const LANDING_CSS = `
/* 히어로 */
.hero{padding:74px 0 58px;background:linear-gradient(180deg,#EDF2FA 0%,var(--bg) 100%);text-align:center}
.hero h1{font-size:clamp(30px,5.6vw,48px);font-weight:900;letter-spacing:-1.4px;line-height:1.26;margin:16px 0 18px}
.hero h1 .hl{color:var(--navy);background:linear-gradient(180deg,transparent 62%,rgba(61,123,217,.22) 62%)}
.hero .lead{margin:0 auto;font-size:clamp(15px,2.2vw,17px)}
.dl-row{display:flex;flex-wrap:wrap;gap:12px;justify-content:center;margin:32px 0 14px}
.btn-dl{display:inline-flex;align-items:center;gap:9px;background:var(--navy);color:#fff;
  border:1px solid var(--navy);border-radius:13px;padding:14px 26px;font-size:16px;font-weight:700;
  box-shadow:var(--shadow-lg);transition:transform .18s,background .18s}
.btn-dl:hover{background:#16304F;transform:translateY(-2px);text-decoration:none}
.btn-dl svg{width:19px;height:19px;flex:0 0 auto}
html.os-mac .btn-dl.win,html.os-win .btn-dl.mac{background:var(--panel);color:var(--navy);
  border-color:var(--line);box-shadow:var(--shadow)}
html.os-mac .btn-dl.win:hover,html.os-win .btn-dl.mac:hover{background:#F1F5FA}
.dl-meta{font-size:13px;color:var(--faint);line-height:1.9}
.dl-meta code{font-family:var(--mono);font-size:12px}
.dl-sub{margin-top:8px;font-size:13px}
.dl-sub a{color:var(--muted);font-weight:600}

/* 동작 화면 — 실제 사용 모습을 가상으로 그린 화면(화상회의 위에 자막이 떠 있는 상태) */
.demo{padding-top:0}
.demo-frame{position:relative;max-width:760px;margin:0 auto;aspect-ratio:16/9;
  border-radius:var(--radius);overflow:hidden;background:#0A0E14;
  box-shadow:var(--shadow-lg);border:1px solid rgba(255,255,255,.07)}

/* 배경 장면: 조명이 은은하게 깔린 회의 화면을 그라데이션으로 표현 */
.scene{position:absolute;inset:0;
  background:
    radial-gradient(70% 55% at 50% 22%,rgba(90,130,190,.30) 0%,transparent 62%),
    radial-gradient(50% 45% at 12% 82%,rgba(60,95,150,.22) 0%,transparent 70%),
    linear-gradient(165deg,#1A2740 0%,#121C2B 52%,#0A0E14 100%)}
.scene::after{content:"";position:absolute;inset:0;
  background:linear-gradient(180deg,transparent 42%,rgba(0,0,0,.55) 100%)}

/* 말하는 사람 실루엣(초점이 나간 영상처럼 살짝 흐리게) */
.figure{position:absolute;left:50%;bottom:0;transform:translateX(-50%);
  width:31%;max-width:205px;height:66%;filter:blur(1.1px)}
.figure .head{position:absolute;left:50%;top:0;transform:translateX(-50%);
  width:45%;aspect-ratio:1;border-radius:50%;
  background:linear-gradient(155deg,#6E86A8 0%,#4A5F7E 55%,#33445E 100%)}
.figure .body{position:absolute;left:50%;top:30%;bottom:0;transform:translateX(-50%);
  width:100%;border-radius:50% 50% 0 0;
  background:linear-gradient(160deg,#5B7397 0%,#3C4F6C 45%,#26344A 100%)}

/* 작은 내 화면 */
.selfview{position:absolute;right:14px;top:14px;width:17%;max-width:112px;aspect-ratio:16/10;
  border-radius:9px;overflow:hidden;background:linear-gradient(170deg,#27374F,#141D2B);
  border:1px solid rgba(255,255,255,.10)}
.selfview .head{position:absolute;left:50%;top:22%;transform:translateX(-50%);
  width:26%;aspect-ratio:1;border-radius:50%;background:rgba(255,255,255,.22)}
.selfview .body{position:absolute;left:50%;bottom:0;transform:translateX(-50%);
  width:64%;height:40%;border-radius:50% 50% 0 0;background:rgba(255,255,255,.16)}

/* 번역 중임을 알리는 표시 */
.livetag{position:absolute;left:14px;top:14px;display:inline-flex;align-items:center;gap:7px;
  padding:6px 12px;border-radius:999px;font-size:12px;font-weight:700;color:#EAF1FC;
  background:rgba(10,16,24,.55);border:1px solid rgba(255,255,255,.12);
  backdrop-filter:blur(8px);-webkit-backdrop-filter:blur(8px)}
.livetag em{width:7px;height:7px;border-radius:50%;background:#4ADE80;font-style:normal;
  animation:pulse 2.2s ease-in-out infinite}
@keyframes pulse{0%,100%{opacity:1;transform:scale(1)}50%{opacity:.35;transform:scale(.82)}}

/* 회의 앱 아래쪽 버튼 줄 */
.callbar{position:absolute;left:50%;bottom:14px;transform:translateX(-50%);
  display:flex;gap:9px;padding:8px 13px;border-radius:999px;
  background:rgba(10,16,24,.5);border:1px solid rgba(255,255,255,.10);
  backdrop-filter:blur(8px);-webkit-backdrop-filter:blur(8px)}
.callbar i{width:21px;height:21px;border-radius:50%;background:rgba(255,255,255,.18);display:block}
.callbar i.off{background:#E5484D;opacity:.85}

/* 화면 위에 떠 있는 자막 */
.sub-overlay{position:absolute;left:50%;bottom:72px;transform:translateX(-50%);
  width:min(84%,590px);height:128px;overflow:hidden;
  display:flex;align-items:flex-start;padding:10px 18px;
  background:rgba(0,0,0,.44);border-radius:12px;
  backdrop-filter:blur(3px);-webkit-backdrop-filter:blur(3px)}
.demo-roll{width:100%;animation:roll 16s ease-in-out infinite}
.demo-block{padding:4px 0 8px}
.demo-tl{font-size:19px;font-weight:800;line-height:1.35;color:#fff;letter-spacing:-.2px;
  text-shadow:0 1px 3px rgba(0,0,0,.95),0 0 10px rgba(0,0,0,.7)}
.demo-block.alt .demo-tl{color:#FFD866}
.demo-src{font-size:12px;color:#C3CCD8;line-height:1.4;margin-top:3px;
  text-shadow:0 1px 3px rgba(0,0,0,.9)}
@keyframes roll{
  0%{transform:translateY(0);opacity:0}
  3%{transform:translateY(0);opacity:1}
  28%{transform:translateY(0);opacity:1}
  34%{transform:translateY(-58px)}
  59%{transform:translateY(-58px)}
  65%{transform:translateY(-116px)}
  94%{transform:translateY(-116px);opacity:1}
  99%{transform:translateY(-116px);opacity:0}
  100%{transform:translateY(0);opacity:0}
}
@media(prefers-reduced-motion:reduce){.demo-roll{animation:none;transform:translateY(-116px)}}
@media(max-width:640px){
  .sub-overlay{bottom:56px;height:106px;width:90%;padding:8px 12px}
  .demo-tl{font-size:14px}
  .demo-src{font-size:10.5px}
    @keyframes roll{
    0%{transform:translateY(0);opacity:0}
    3%{transform:translateY(0);opacity:1}
    28%{transform:translateY(0);opacity:1}
    34%{transform:translateY(-49px)}
    59%{transform:translateY(-49px)}
    65%{transform:translateY(-98px)}
    94%{transform:translateY(-98px);opacity:1}
    99%{transform:translateY(-98px);opacity:0}
    100%{transform:translateY(0);opacity:0}
  }
  .livetag{font-size:10.5px;padding:5px 9px}
  .callbar{bottom:10px;padding:6px 10px;gap:7px}
  .callbar i{width:16px;height:16px}
}
.demo-cap{text-align:center;font-size:13px;color:var(--faint);margin-top:14px}

/* 카드 그리드 */
.grid{display:grid;gap:16px;margin-top:34px}
.g3{grid-template-columns:repeat(3,1fr)}
.g2{grid-template-columns:repeat(2,1fr)}
.card{background:var(--panel);border:1px solid var(--line);border-radius:var(--radius);padding:26px 24px;box-shadow:var(--shadow)}
.card h3{margin-bottom:7px}
.card p{font-size:14.5px;color:var(--muted);line-height:1.72}
.num{display:inline-flex;align-items:center;justify-content:center;width:30px;height:30px;border-radius:9px;
  background:var(--navy);color:#fff;font-size:14px;font-weight:800;margin-bottom:13px}
.dot{display:inline-block;width:8px;height:8px;border-radius:50%;background:var(--blue);margin-bottom:14px}
@media(max-width:860px){.g3{grid-template-columns:1fr}.g2{grid-template-columns:1fr}}

.alt-bg{background:var(--panel);border-top:1px solid var(--line);border-bottom:1px solid var(--line)}

/* 표 */
.tbl-wrap{margin-top:30px;overflow-x:auto;border:1px solid var(--line);border-radius:var(--radius);background:var(--panel)}
table{width:100%;border-collapse:collapse;font-size:14.5px;min-width:560px}
th,td{padding:15px 18px;text-align:left;border-bottom:1px solid var(--line);vertical-align:top}
thead th{font-size:13px;font-weight:800;color:var(--faint);background:#FAFCFE}
tbody tr:last-child td{border-bottom:0}
td.ok{color:var(--green);font-weight:600}
td.no{color:var(--muted)}

/* 비용 · 개인정보 */
.note{background:var(--panel);border:1px solid var(--line);border-left:3px solid var(--blue);
  border-radius:var(--radius-sm);padding:20px 22px;margin-top:22px}
.note p{font-size:14.5px;color:var(--muted);line-height:1.75}
.note p+p{margin-top:10px}
.note strong{color:var(--ink)}
.plist{margin-top:26px;display:grid;gap:14px}
.plist li{list-style:none;display:flex;gap:12px;font-size:14.5px;color:var(--muted);line-height:1.7}
.plist .mark{flex:0 0 auto;width:20px;height:20px;border-radius:50%;background:var(--blue-soft);
  color:var(--blue);font-size:12px;font-weight:800;display:flex;align-items:center;justify-content:center;margin-top:3px}
.plist b{color:var(--ink);font-weight:700}

/* FAQ */
details{background:var(--panel);border:1px solid var(--line);border-radius:var(--radius-sm);
  padding:0;margin-bottom:10px;overflow:hidden}
summary{cursor:pointer;list-style:none;padding:17px 22px;font-size:15.5px;font-weight:700;
  display:flex;justify-content:space-between;gap:14px;align-items:center}
summary::-webkit-details-marker{display:none}
summary::after{content:"+";color:var(--faint);font-weight:400;font-size:20px;flex:0 0 auto}
details[open] summary::after{content:"−"}
details p{padding:0 22px 19px;font-size:14.5px;color:var(--muted);line-height:1.75}

/* 마무리 CTA */
.final{text-align:center;background:linear-gradient(180deg,var(--bg) 0%,#E9EFF8 100%)}
.final h2{margin-bottom:10px}
`;

function renderLanding(version: string): string {
	const TITLE = "Cross-liveTranslate — 컴퓨터 소리를 실시간 번역 자막으로 (맥·윈도우 무료)";
	const DESC =
		"화상회의와 영상에서 나오는 외국어를 실시간 번역 자막으로 화면 위에 띄웁니다. macOS 12 이상, Windows 10·11에서 동작하고 가상 오디오 장치 설치가 필요 없습니다. 무료, 회원가입 없음.";

	const softwareLd = JSON.stringify({
		"@context": "https://schema.org",
		"@type": "SoftwareApplication",
		name: "Cross-liveTranslate",
		alternateName: "크로스 라이브트랜슬레이트",
		applicationCategory: "UtilitiesApplication",
		applicationSubCategory: "실시간 자막 번역",
		operatingSystem: "macOS 12 이상, Windows 10, Windows 11",
		softwareVersion: version,
		url: SITE + "/",
		downloadUrl: SITE + "/#download",
		installUrl: SITE + "/#download",
		softwareHelp: REPO,
		releaseNotes: RELEASES + "/latest",
		inLanguage: "ko",
		image: SITE + "/assets/icon.png",
		description: DESC,
		offers: { "@type": "Offer", price: "0", priceCurrency: "KRW" },
		author: { "@type": "Person", name: "zerolive" },
		featureList: [
			"시스템 오디오 직접 캡처 (macOS Core Audio Process Tap · Windows WASAPI 루프백)",
			"영화 자막식 롤업 오버레이 (항상 위 · 클릭 통과)",
			"화자 전환 두 가지 색 구분",
			"원문과 번역문 동시 표시",
			"번역 음성 재생과 원음 자동 줄임",
			"자막 텍스트 파일 녹화",
			"자막 글꼴·색·위치·모니터 설정",
			"실시간 사용 금액 표시",
			"서명 검증 자동 업데이트",
		],
		softwareRequirements: "Google Gemini API 키, 인터넷 연결",
	});

	const faqLd = JSON.stringify({
		"@context": "https://schema.org",
		"@type": "FAQPage",
		mainEntity: FAQ.map((f) => ({
			"@type": "Question",
			name: f.q,
			acceptedAnswer: { "@type": "Answer", text: f.a },
		})),
	});

	const orgLd = JSON.stringify({
		"@context": "https://schema.org",
		"@type": "Organization",
		name: "zerolive.co.kr",
		url: SITE + "/",
		logo: SITE + "/assets/icon.png",
		email: CONTACT_EMAIL,
		sameAs: [REPO],
	});

	const dlIcon =
		'<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.1" stroke-linecap="round" stroke-linejoin="round"><path d="M12 3v12"/><path d="m7 11 5 5 5-5"/><path d="M4 21h16"/></svg>';

	const faqHtml = FAQ.map(
		(f) => `<details><summary>${f.q}</summary><p>${f.a}</p></details>`,
	).join("\n      ");

	const body = `
<main>

  <section class="hero">
    <div class="wrap">
      <span class="eyebrow">macOS · Windows</span>
      <h1>컴퓨터에서 나는 소리를<br><span class="hl">실시간 번역 자막</span>으로</h1>
      <p class="lead">화상회의와 영상에서 흘러나오는 외국어를 그대로 받아 화면 위에 자막으로 띄웁니다.<br>
      가상 오디오 장치를 따로 설치하지 않아도 됩니다.</p>

      <div class="dl-row">
        <a class="btn-dl mac" id="dl-mac" href="/download/mac">${dlIcon}macOS용 내려받기</a>
        <a class="btn-dl win" id="dl-win" href="/download/win">${dlIcon}Windows용 내려받기</a>
      </div>
      <p class="dl-meta">
        v${version} · 무료 · 회원가입 없음<br>
        macOS 12 이상 (Intel·Apple Silicon) · Windows 10·11 64비트
      </p>
      <p class="dl-sub"><a href="/download/win-portable">Windows 포터블(설치 없이 실행)</a> · <a href="${RELEASES}" rel="noopener">지난 버전 보기</a></p>
    </div>
  </section>

  <section class="demo">
    <div class="wrap">
      <div class="demo-frame" role="img"
           aria-label="화상회의 화면 위에 번역 자막이 떠 있는 모습. 번역문 아래에 원문이 함께 표시되고, 말하는 사람이 바뀌면 자막 색이 바뀝니다.">
        <div class="scene"></div>
        <div class="figure"><span class="head"></span><span class="body"></span></div>
        <div class="selfview"><span class="head"></span><span class="body"></span></div>
        <div class="livetag"><em></em>실시간 번역 중 · 영어 → 한국어</div>
        <div class="sub-overlay">
          <div class="demo-roll">
            <div class="demo-block">
              <div class="demo-tl">안녕하세요, 오늘 회의를 시작하겠습니다.</div>
              <div class="demo-src">Hello everyone, let's get today's meeting started.</div>
            </div>
            <div class="demo-block alt">
              <div class="demo-tl">먼저 지난주 진행 상황부터 공유해 주세요.</div>
              <div class="demo-src">First, please share last week's progress.</div>
            </div>
            <div class="demo-block">
              <div class="demo-tl">저희 팀은 결제 화면 개편을 마쳤습니다.</div>
              <div class="demo-src">Our team finished the checkout redesign.</div>
            </div>
            <div class="demo-block alt">
              <div class="demo-tl">좋습니다. 배포 일정은 언제인가요?</div>
              <div class="demo-src">Great. When is the release scheduled?</div>
            </div>
          </div>
        </div>
        <div class="callbar"><i></i><i></i><i></i><i></i><i class="off"></i></div>
      </div>
      <p class="demo-cap">자막이 뜨는 모습을 그림으로 나타냈습니다. 말하는 사람이 바뀌면 색이 바뀌고, 번역문 아래에 원문이 함께 붙습니다.</p>
    </div>
  </section>

  <section class="alt-bg">
    <div class="wrap">
      <span class="eyebrow">이런 때 씁니다</span>
      <h2>자막이 없어서 놓치던 자리</h2>
      <div class="grid g3">
        <div class="card"><span class="dot"></span>
          <h3>해외 팀과의 화상회의</h3>
          <p>Zoom, Microsoft Teams, Google Meet에서 나오는 말을 곧바로 한국어 자막으로 봅니다. 회의 창을 가리지 않고 화면 위에 떠 있습니다.</p>
        </div>
        <div class="card"><span class="dot"></span>
          <h3>자막 없는 영상과 강의</h3>
          <p>유튜브 영상, 온라인 강의, 팟캐스트처럼 자막이 없거나 자동 자막이 부정확한 소리를 번역해 줍니다.</p>
        </div>
        <div class="card"><span class="dot"></span>
          <h3>눈앞에서 오가는 대화</h3>
          <p>입력을 마이크로 바꾸면 회의실이나 현장에서 오가는 외국어 대화도 자막으로 따라갈 수 있습니다.</p>
        </div>
      </div>
    </div>
  </section>

  <section>
    <div class="wrap">
      <span class="eyebrow">시작하기</span>
      <h2>세 단계면 자막이 뜹니다</h2>
      <div class="grid g3">
        <div class="card"><span class="num">1</span>
          <h3>API 키 넣기</h3>
          <p>Google AI Studio에서 받은 Gemini API 키를 설정 창에 붙여 넣습니다. 키는 운영체제의 자격 증명 저장소에 보관됩니다.</p>
        </div>
        <div class="card"><span class="num">2</span>
          <h3>소리 잡을 곳 고르기</h3>
          <p>컴퓨터에서 나는 소리와 마이크 중에 고릅니다. 자동으로 두면 상황에 맞게 알아서 잡습니다.</p>
        </div>
        <div class="card"><span class="num">3</span>
          <h3>시작 누르기</h3>
          <p>화면 위에 자막이 뜹니다. 글꼴과 위치, 표시할 모니터는 설정에서 언제든 바꿉니다.</p>
        </div>
      </div>
    </div>
  </section>

  <section id="features" class="alt-bg">
    <div class="wrap">
      <span class="eyebrow">기능</span>
      <h2>실시간 자막 번역 프로그램에 필요한 것</h2>
      <div class="grid g2">
        <div class="card"><h3>시스템 소리를 바로 받습니다</h3>
          <p>macOS는 Core Audio Process Tap, Windows는 WASAPI 루프백을 씁니다. 가상 오디오 장치 설치나 화면 녹화 권한이 필요 없고, 기본 출력 장치도 그대로 둡니다.</p></div>
        <div class="card"><h3>영화 자막처럼 굴러갑니다</h3>
          <p>새 문장이 아래에서 올라오고 지난 문장이 위로 밀립니다. 항상 맨 앞에 뜨지만 마우스 클릭은 그대로 통과해 작업을 막지 않습니다.</p></div>
        <div class="card"><h3>말하는 사람이 바뀌면 색이 바뀝니다</h3>
          <p>말이 끊긴 구간과 질문에서 답변으로 넘어가는 흐름을 보고 화자 전환을 짚어, 두 가지 색으로 번갈아 보여줍니다.</p></div>
        <div class="card"><h3>원문도 함께 봅니다</h3>
          <p>번역문 아래에 원문을 짝지어 보여줍니다. 받아쓰기와 번역을 나란히 확인할 수 있고, 필요 없으면 끕니다.</p></div>
        <div class="card"><h3>번역된 말을 소리로 듣습니다</h3>
          <p>자막 대신 귀로 듣고 싶을 때 번역 음성을 재생합니다. 재생하는 동안 원래 소리는 자동으로 작아집니다.</p></div>
        <div class="card"><h3>자막을 파일로 남깁니다</h3>
          <p>확정된 자막을 <code>[시각] 원문 → 번역문</code> 형식의 텍스트 파일로 저장합니다. 회의록 초안으로 그대로 씁니다.</p></div>
        <div class="card"><h3>자막을 원하는 대로 꾸밉니다</h3>
          <p>글꼴, 크기, 굵기, 글자색, 외곽선, 배경, 정렬, 최대 줄 수를 바꿉니다. 여러 모니터 중 어디에 띄울지, 위아래 어디에 둘지도 고릅니다.</p></div>
        <div class="card"><h3>지금 얼마 썼는지 보입니다</h3>
          <p>이번 사용분과 누적 금액을 화면에 띄웁니다. 말이 없는 구간은 보내지 않아 조용한 시간의 요금이 붙지 않습니다.</p></div>
      </div>
    </div>
  </section>

  <section>
    <div class="wrap">
      <span class="eyebrow">차이점</span>
      <h2>설치할 게 앱 하나뿐입니다</h2>
      <p class="lead">시스템 소리를 받으려고 가상 오디오 장치를 깔고 출력 장치를 바꾸는 준비 과정이 없습니다.</p>
      <div class="tbl-wrap">
        <table>
          <thead><tr><th>항목</th><th>흔한 방식</th><th>Cross-liveTranslate</th></tr></thead>
          <tbody>
            <tr><td>시스템 소리 캡처</td><td class="no">가상 오디오 장치를 따로 설치</td><td class="ok">운영체제 기능으로 바로 받음</td></tr>
            <tr><td>기본 출력 장치</td><td class="no">가상 장치로 바꿔야 소리가 잡힘</td><td class="ok">쓰던 스피커·헤드폰 그대로</td></tr>
            <tr><td>화면 녹화 권한</td><td class="no">요구하는 경우 있음</td><td class="ok">필요 없음</td></tr>
            <tr><td>지원 운영체제</td><td class="no">한쪽만 지원하는 경우가 많음</td><td class="ok">macOS와 Windows 모두</td></tr>
            <tr><td>계정</td><td class="no">회원가입·로그인 필요</td><td class="ok">계정 없음</td></tr>
          </tbody>
        </table>
      </div>
    </div>
  </section>

  <section id="cost" class="alt-bg">
    <div class="wrap">
      <span class="eyebrow">비용</span>
      <h2>앱은 무료, 번역은 본인 API 키</h2>
      <p class="lead">숨은 결제나 구독이 없습니다. 대신 번역에 쓰이는 Google Gemini API 요금이 직접 발급받은 키로 나갑니다.</p>
      <div class="note">
        <p><strong>요금 기준</strong> — Gemini 3.5 Live Translate 모델은 오디오 입력 100만 토큰당 3.50달러, 오디오 출력 100만 토큰당 21.00달러입니다.</p>
        <p><strong>알아 두실 점</strong> — 출력 오디오는 재생하지 않아도 만들어지고 요금이 붙습니다. 말이 없는 구간도 마찬가지여서, 말이 있는 구간만 보내는 기능이 기본으로 켜져 있습니다.</p>
        <p><strong>확인 방법</strong> — 앱 화면에 이번 사용분과 누적 금액이 표시됩니다. 실제 청구 금액은 Google AI Studio에서도 확인할 수 있습니다.</p>
      </div>
    </div>
  </section>

  <section>
    <div class="wrap">
      <span class="eyebrow">개인정보</span>
      <h2>무엇이 어디로 가는지 그대로 씁니다</h2>
      <ul class="plist">
        <li><span class="mark">1</span><span><b>소리는 Google Gemini API로 전송됩니다.</b> 번역을 위해 필요한 과정입니다. 이 앱은 자체 서버를 두지 않으며, 만든 사람에게 소리나 자막이 전달되지 않습니다.</span></li>
        <li><span class="mark">2</span><span><b>API 키는 운영체제 자격 증명 저장소에 보관됩니다.</b> macOS는 키체인, Windows는 자격 증명 관리자를 씁니다. 설정 파일에 평문으로 남지 않습니다.</span></li>
        <li><span class="mark">3</span><span><b>자막 녹화 파일은 내 컴퓨터에만 남습니다.</b> 지정한 폴더에 저장되고 어디로도 올라가지 않습니다.</span></li>
        <li><span class="mark">4</span><span><b>계정이 없습니다.</b> 회원가입, 로그인, 사용 기록 수집이 없습니다.</span></li>
      </ul>
      <p class="dl-sub" style="margin-top:22px"><a href="/privacy">개인정보처리방침 전문 보기</a></p>
    </div>
  </section>

  <section id="faq" class="alt-bg">
    <div class="wrap">
      <span class="eyebrow">자주 묻는 질문</span>
      <h2>설치 전에 확인하실 내용</h2>
      <div style="margin-top:30px">
      ${faqHtml}
      </div>
    </div>
  </section>

  <section id="download" class="final">
    <div class="wrap">
      <h2>지금 받아서 바로 써 보세요</h2>
      <p class="lead" style="margin:0 auto">설치하고 API 키만 넣으면 됩니다. 회원가입도 결제 정보 입력도 없습니다.</p>
      <div class="dl-row">
        <a class="btn-dl mac" href="/download/mac">${dlIcon}macOS용 내려받기</a>
        <a class="btn-dl win" href="/download/win">${dlIcon}Windows용 내려받기</a>
      </div>
      <p class="dl-meta">v${version} · macOS 12 이상 · Windows 10·11 64비트</p>
      <p class="dl-sub"><a href="/download/win-portable">Windows 포터블</a> · <a href="${REPO}" rel="noopener">소스 코드와 릴리스 기록</a></p>
    </div>
  </section>

</main>
<script>
(function(){
  var ua = navigator.userAgent || "";
  var p = navigator.platform || "";
  var isMac = /Mac/i.test(p) || /Mac OS X/i.test(ua);
  var isWin = /Win/i.test(p) || /Windows/i.test(ua);
  if (isMac && !isWin) document.documentElement.className += " os-mac";
  else if (isWin) document.documentElement.className += " os-win";
})();
</script>`;

	return shell({
		title: TITLE,
		desc: DESC,
		path: "/",
		css: LANDING_CSS,
		jsonld: [softwareLd, faqLd, orgLd],
		body,
	});
}

// ─────────────────────────────────────────────────────────────
// 개인정보처리방침
// ─────────────────────────────────────────────────────────────
const PRIVACY_CSS = `
.doc{padding:60px 0 20px;max-width:760px;margin:0 auto}
.doc h1{font-size:30px;font-weight:900;letter-spacing:-.8px;margin-bottom:8px}
.doc .upd{font-size:13px;color:var(--faint);margin-bottom:34px}
.doc h2{font-size:19px;margin:36px 0 10px}
.doc p,.doc li{font-size:15px;color:var(--muted);line-height:1.8}
.doc p+p{margin-top:10px}
.doc ul{margin:10px 0 0 20px}
.doc li{margin-bottom:7px}
.doc b{color:var(--ink)}
.doc code{font-family:var(--mono);font-size:13px;background:#EEF2F8;padding:2px 6px;border-radius:5px;color:var(--ink)}
`;

function renderPrivacy(): string {
	const body = `
<main class="wrap"><article class="doc">
  <h1>개인정보처리방침</h1>
  <p class="upd">Cross-liveTranslate · 최종 수정일 2026년 9월 4일</p>

  <p>Cross-liveTranslate(이하 “이 앱”)는 계정을 만들지 않고 쓰는 프로그램입니다. 이 앱을 만든 사람은 이용자의 개인정보를 수집하거나 보관하는 서버를 운영하지 않습니다. 아래는 이 앱이 어떤 정보를 어떻게 다루는지 정리한 내용입니다.</p>

  <h2>1. 수집하지 않는 정보</h2>
  <p>이 앱은 회원가입, 로그인, 이메일 수집을 하지 않습니다. 사용 기록이나 통계를 만든 사람에게 보내지 않으며, 광고나 분석 도구를 넣지 않았습니다.</p>

  <h2>2. 소리와 자막이 처리되는 방식</h2>
  <ul>
    <li><b>번역을 위한 전송</b> — 번역하려면 캡처한 소리를 Google의 Gemini API로 보내야 합니다. 번역 결과도 같은 경로로 돌아옵니다. 이 과정은 이용자가 직접 발급받은 API 키로 이루어집니다.</li>
    <li><b>전송 대상</b> — 소리는 Google에만 전달됩니다. 이 앱을 만든 사람의 서버를 거치지 않으며, 만든 사람은 소리나 자막 내용을 볼 수 없습니다.</li>
    <li><b>Google의 처리</b> — 전송된 데이터를 Google이 어떻게 다루는지는 <a href="https://policies.google.com/privacy" rel="noopener">Google 개인정보처리방침</a>과 <a href="https://ai.google.dev/gemini-api/terms" rel="noopener">Gemini API 약관</a>을 따릅니다.</li>
    <li><b>앱 안에서의 보관</b> — 화면에 표시한 자막은 메모리에만 있고, 번역을 멈추거나 앱을 끄면 사라집니다.</li>
  </ul>

  <h2>3. 이용자 컴퓨터에만 저장되는 것</h2>
  <ul>
    <li><b>API 키</b> — 운영체제의 자격 증명 저장소에 보관합니다. macOS는 키체인, Windows는 자격 증명 관리자를 씁니다. 설정 파일에 평문으로 기록하지 않습니다.</li>
    <li><b>설정 값</b> — 언어, 입력 장치, 자막 모양과 위치 같은 설정을 <code>settings.json</code> 파일로 저장합니다.</li>
    <li><b>자막 녹화 파일</b> — 녹화를 켰을 때만 이용자가 지정한 폴더에 텍스트 파일로 저장합니다. 어디로도 전송되지 않습니다.</li>
    <li><b>진단 기록</b> — 문제를 찾기 위한 동작 기록을 <code>~/.liveTranslate</code> 폴더에 남깁니다. 이 파일도 이용자 컴퓨터에만 있으며, 이용자가 직접 보내지 않는 한 밖으로 나가지 않습니다.</li>
  </ul>
  <p>위 파일은 모두 이용자가 직접 지우거나 앱의 초기화 기능으로 지울 수 있습니다.</p>

  <h2>4. 인터넷에 연결하는 경우</h2>
  <ul>
    <li><b>번역</b> — Google Gemini API에 연결합니다.</li>
    <li><b>업데이트 확인</b> — 새 버전이 있는지 확인하려고 GitHub Releases에 접속합니다. 이때 일반적인 웹 요청 정보(접속 IP 등)가 GitHub에 남을 수 있습니다. 자동 확인은 설정에서 끌 수 있습니다.</li>
    <li><b>내려받은 파일 검증</b> — 업데이트 파일은 서명을 확인한 뒤에만 설치합니다.</li>
  </ul>

  <h2>5. 이 웹사이트</h2>
  <p>이 랜딩 페이지는 Cloudflare Workers로 제공됩니다. 방문자 추적 도구나 광고를 넣지 않았습니다. 다만 서비스 운영과 보안을 위해 Cloudflare가 접속 기록을 일정 기간 보관할 수 있습니다.</p>

  <h2>6. 만 14세 미만 이용자</h2>
  <p>이 앱은 만 14세 미만 이용자를 대상으로 하지 않으며, 해당 연령대의 정보를 의도적으로 수집하지 않습니다.</p>

  <h2>7. 방침이 바뀔 때</h2>
  <p>내용이 바뀌면 이 페이지에 새 내용과 수정일을 올립니다.</p>

  <h2>8. 문의</h2>
  <p>궁금한 점은 <a href="mailto:${CONTACT_EMAIL}">${CONTACT_EMAIL}</a>로 보내 주시거나 <a href="${REPO}/issues" rel="noopener">GitHub 이슈</a>에 남겨 주세요.</p>
</article></main>`;

	return shell({
		title: "개인정보처리방침 — Cross-liveTranslate",
		desc: "Cross-liveTranslate는 계정 없이 쓰는 프로그램입니다. 소리는 번역을 위해 Google Gemini API로만 전송되고, API 키와 자막 녹화 파일은 이용자 컴퓨터에만 저장됩니다.",
		path: "/privacy",
		css: PRIVACY_CSS,
		body,
	});
}
