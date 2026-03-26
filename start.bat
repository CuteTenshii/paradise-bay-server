@echo off
go build -trimpath -ldflags="-s -w" .
.\paradise-bay-server.exe
pause