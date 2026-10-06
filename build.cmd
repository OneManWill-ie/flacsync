@echo off
rem Double-clickable wrapper for build.ps1. Run "build.cmd -?" for options.
powershell -NoProfile -ExecutionPolicy Bypass -File "%~dp0build.ps1" %*
exit /b %ERRORLEVEL%
