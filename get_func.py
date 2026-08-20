import sys

with open('internal/api/server.go') as f:
    lines = f.readlines()

in_func = False
for line in lines:
    if 'func (s *Server) handleVoiceSpeak' in line:
        in_func = True
    if in_func:
        print(line, end='')
        if line.startswith('}'):
            break
