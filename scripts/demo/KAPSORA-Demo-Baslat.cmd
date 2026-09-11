@echo off
rem KAPSORA demo: the three web apps on this computer, each with its own sample data in the
rem browser. No server and no database are needed. KAPSORA-Demo-Durdur.cmd stops them.
rem Ports 5173 and 8080 belong to another project on this computer; the demo keeps clear of them.
setlocal
title KAPSORA baslatiliyor
cd /d "C:\CELIKBROS PROJECTS\kapsora"

rem Already running? Then only open the browser, never a second set of servers.
powershell -NoProfile -Command "if (Get-NetTCPConnection -LocalPort 5181 -State Listen -ErrorAction SilentlyContinue) { exit 1 } else { exit 0 }"
if errorlevel 1 goto open

start "KAPSORA Demo - Yonetim paneli" /min cmd /k pnpm --filter @kapsora/backoffice exec vite --port 5181 --strictPort --host 127.0.0.1
start "KAPSORA Demo - Saglayici portali" /min cmd /k pnpm --filter @kapsora/provider exec vite --port 5182 --strictPort --host 127.0.0.1
start "KAPSORA Demo - Uye uygulamasi" /min cmd /k pnpm --filter @kapsora/member exec vite --port 5183 --strictPort --host 127.0.0.1
echo KAPSORA demo aciliyor, birkac saniye bekleyin...
timeout /t 12 /nobreak >nul

:open
start "" "C:\CELIKBROS PROJECTS\kapsora\scripts\demo\KAPSORA-Demo-Rehberi.html"
start "" http://127.0.0.1:5181/
start "" http://127.0.0.1:5182/
start "" http://127.0.0.1:5183/
endlocal