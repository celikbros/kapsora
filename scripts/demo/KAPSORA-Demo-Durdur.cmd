@echo off
rem Stops the KAPSORA demo started by KAPSORA-Demo-Baslat.cmd. Only two kinds of process are
rem stopped: the node servers of the three demo apps (vite or pnpm on ports 5181-5183 inside the
rem kapsora folder) and the cmd windows that started them ("cmd /k pnpm --filter @kapsora/...").
rem The port may be written --port 5181 or "--port" "5181"; both are recognised. Nothing else
rem is touched, not even another program that happens to mention the same words.
title KAPSORA durdur
powershell -NoProfile -ExecutionPolicy Bypass -Command "$p = @(Get-CimInstance Win32_Process -Filter \"Name='node.exe' OR Name='cmd.exe'\" | Where-Object { $c = $_.CommandLine; $c -and $c -match '--port\W+518[123]\b' -and (($_.Name -eq 'cmd.exe' -and $c -match '^\S*cmd(\.exe)?\S*\s+/k\s+pnpm\s+--filter\s+@kapsora/') -or ($_.Name -eq 'node.exe' -and $c -match '\\kapsora\\' -and $c -match 'vite|pnpm')) }); $p | ForEach-Object { Stop-Process -Id $_.ProcessId -Force -ErrorAction SilentlyContinue }; Write-Host ('KAPSORA demo durduruldu (' + $p.Count + ' surec).')"
timeout /t 3 /nobreak >nul