# premark

A GitHub-flavored markdown previewer. 
Run it next to your `.md` files and open the local URL in your browser.
Then, you'll see updates on save.

## Installation

```
go install github.com/broothie/premark@latest
```

## Usage

```
$ premark -h
Usage of premark:
  -p int
    	port to run server on (default 8888)
  -w string
    	glob of files to watch (default "**/**.md")
$ premark
premark running at http://localhost:8888
watching README.md: http://localhost:8888?filename=README.md
```
