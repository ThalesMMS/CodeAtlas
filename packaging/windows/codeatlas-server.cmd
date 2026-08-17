@echo off
setlocal
start "" /b /wait "%~dp0codeatlas.exe" -desktop=false %*
exit /b %ERRORLEVEL%
