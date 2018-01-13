# flactomp3

flactomp3 is a Golang port of a shell script I wrote to recursively convert a directory of flac files to mp3.

It takes two flags:

- `-recurse` (default `true`) that controls whether or not it descends recursively into sub-directories, and
- `-out` (default `"{dir}.{@file}.mp3"`) which sets the output path (see newPath in [main.go](/main.go) for the valid tags).

It also accepts a directory to operate in, otherwise it uses the working directory.

It requires metaflac to extract metadata from the input flac file, and a combination of flac and lame to convert the file to mp3.