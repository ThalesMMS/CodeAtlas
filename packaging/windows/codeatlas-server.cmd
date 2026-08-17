@echo off
setlocal
"%~dp0codeatlas-server.exe" -desktop=false %*
exit /b %ERRORLEVEL%
