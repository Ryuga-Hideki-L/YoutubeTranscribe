import express from "express";
import fs from "fs";
import dotenv from "dotenv";
import { YoutubeTranscript } from "youtube-transcript";

dotenv.config();

const app = express();
const PORT = 3000;
const API_KEY = process.env.YOUTUBE_API_KEY;

// ---- настройки ----
const MAX_VIDEOS = 200;
const BASE_DELAY = 900;
const RANDOM_DELAY = 600;
const MAX_RETRIES = 3;
const RETRY_DELAY = 1000;
const COOLDOWN_TIME = 60000;
const PAGE_DELAY = 2500;

// ---- лог ----
const LOG_FILE = "logs.txt";

function writeLog(text) {
  const line = `[${new Date().toISOString()}] ${text}\n`;
  fs.appendFileSync(LOG_FILE, line);
  console.log(text);
}

// ---- хранилище ----
let videoStorage = { videos: [] };

if (fs.existsSync("cache.json")) {
  try {
    const raw = fs.readFileSync("cache.json", "utf-8");
    if (raw.trim()) videoStorage = JSON.parse(raw);
  } catch {
    videoStorage = { videos: [] };
  }
}

function saveData() {
  fs.writeFileSync("cache.json", JSON.stringify(videoStorage, null, 2));
}

// ---- статистика ----
let stats = {
  processed: 0,
  success: 0,
  noSubs: 0,
  errors: 0,
  status: "idle",
  info: ""
};

// ---- utils ----
const sleep = (ms) => new Promise(r => setTimeout(r, ms));

function randomDelay() {
  return BASE_DELAY + Math.floor(Math.random() * RANDOM_DELAY);
}

// ---- найти канал ----
async function findChannelId(name) {
  const url = `https://www.googleapis.com/youtube/v3/search?part=snippet&type=channel&q=${encodeURIComponent(name)}&key=${API_KEY}`;
  const res = await fetch(url);
  const data = await res.json();
  return data.items?.[0]?.snippet?.channelId || null;
}

// ---- получить субтитры ----
async function getSubtitlesWithRetry(videoId) {
  let delay = RETRY_DELAY;

  for (let i = 1; i <= MAX_RETRIES; i++) {
    try {
      const transcript = await YoutubeTranscript.fetchTranscript(videoId);

      return {
        ok: true,
        subs: transcript.map(x => ({
          text: x.text,
          time: Math.floor(x.offset / 1000)
        }))
      };

    } catch (e) {
      const errorText = String(e.message || e);

      if (
        errorText.includes("Transcript is disabled") ||
        errorText.includes("No transcripts")
      ) {
        return { ok: false, noSubs: true };
      }

      const isBlocked =
        errorText.toLowerCase().includes("too many requests") ||
        errorText.toLowerCase().includes("captcha");

      writeLog(`ошибка попытка ${i}: ${videoId} → ${errorText}`);

      if (isBlocked) {
        stats.info = "ожидание (блокировка)";
        writeLog("⏸ пауза из-за лимита");
        await sleep(COOLDOWN_TIME);
        stats.info = "";
      } else {
        await sleep(delay);
        delay *= 2;
      }

      if (i === MAX_RETRIES) {
        return { ok: false, error: errorText };
      }
    }
  }
}

// ---- индекс ----
app.get("/index", async (req, res) => {
  const { channel } = req.query;
  if (!channel) return res.send("нет канала");

  writeLog("=== старт ===");

  const channelId = await findChannelId(channel);
  if (!channelId) return res.send("канал не найден");

  const uploads = "UU" + channelId.slice(2);

  videoStorage.videos = [];

  stats = {
    processed: 0,
    success: 0,
    noSubs: 0,
    errors: 0,
    status: "loading",
    info: ""
  };

  let nextPage = "";
  let collected = 0;

  while (collected < MAX_VIDEOS) {
    const url = `https://www.googleapis.com/youtube/v3/playlistItems?key=${API_KEY}&playlistId=${uploads}&part=snippet&maxResults=50&pageToken=${nextPage}`;

    const resYT = await fetch(url);
    const data = await resYT.json();

    if (!data.items) break;

    for (let item of data.items) {
      if (collected >= MAX_VIDEOS) break;

      const id = item.snippet?.resourceId?.videoId;
      const title = item.snippet?.title || "без названия";

      if (!id) continue;

      stats.processed++;
      writeLog(`видео: ${id}`);

      const result = await getSubtitlesWithRetry(id);

      if (result.ok && result.subs.length) {
        videoStorage.videos.push({
          id,
          title,
          thumb: item.snippet.thumbnails.medium.url,
          subs: result.subs
        });

        stats.success++;
        collected++;

        writeLog("✅ ок");

      } else if (result.noSubs) {
        stats.noSubs++;
        writeLog("❌ нет субтитров");

      } else {
        stats.errors++;
        writeLog("⚠ ошибка");
      }

      console.log(
        `📊 ${stats.success}/${MAX_VIDEOS} | обработано ${stats.processed} | ❌ ${stats.noSubs} | ⚠ ${stats.errors}`
      );

      await sleep(randomDelay());
    }

    nextPage = data.nextPageToken;
    if (!nextPage) break;

    await sleep(PAGE_DELAY);
  }

  stats.status = "done";
  saveData();

  writeLog("=== конец ===");

  res.send("готово");
});

// ---- прогресс ----
app.get("/progress", (req, res) => {
  res.json(stats);
});

// ---- поиск ----
app.get("/search", (req, res) => {
  const { q } = req.query;
  if (!q) return res.json([]);

  const text = q.toLowerCase();
  let results = [];

  for (let video of videoStorage.videos) {
    for (let line of video.subs) {
      if (line.text.toLowerCase().includes(text)) {
        results.push({
          id: video.id,
          title: video.title,
          thumb: video.thumb,
          time: line.time,
          text: line.text
        });
      }
    }
  }

  res.json(results.slice(0, 50));
});

// ---- UI ----
app.get("/", (req, res) => {
  res.send(`
<!DOCTYPE html>
<html>
<head>
<meta charset="UTF-8">
<title>Поиск по субтитрам</title>
<style>
body{background:#111;color:white;font-family:Arial;margin:0}
header{padding:10px;background:#222}
input,button{padding:10px;border:none;border-radius:6px}
button{background:red;color:white}
.grid{display:grid;grid-template-columns:repeat(auto-fill,minmax(300px,1fr));gap:10px;padding:10px}
.card{background:#222;border-radius:10px;overflow:hidden}
img{width:100%;cursor:pointer}
</style>
</head>
<body>

<header>
<input id="channel" placeholder="канал">
<button onclick="start()">загрузить</button>
<input id="q" placeholder="поиск">
<button onclick="search()">искать</button>
</header>

<div id="stats"></div>
<div id="out" class="grid"></div>

<script>
async function start(){
  const c=document.getElementById("channel").value;
  await fetch("/index?channel="+c);
  update();
}

async function update(){
  const p=await (await fetch("/progress")).json();
  document.getElementById("stats").innerText=
  "обработано:"+p.processed+" | найдено:"+p.success+" | нет:"+p.noSubs+" | ошибки:"+p.errors;
  if(p.status!="done") setTimeout(update,1000);
}

async function search(){
  const q=document.getElementById("q").value;
  const data=await (await fetch("/search?q="+q)).json();

  document.getElementById("out").innerHTML=data.map(d=>\`
  <div class="card">
    <div onclick="this.innerHTML='<iframe width=100% height=200 src=https://www.youtube.com/embed/\${d.id}?start=\${d.time}></iframe>'">
      <img src="https://img.youtube.com/vi/\${d.id}/hqdefault.jpg">
    </div>
    <div style="padding:10px">
      <b>\${d.title}</b><br>
      \${d.time}s<br>
      \${d.text}
    </div>
  </div>\`).join("");
}
</script>

</body>
</html>
`);
});

app.listen(PORT, () => {
  console.log("http://localhost:3000");
});