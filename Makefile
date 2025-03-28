
GO_SRC=main.go
GROUP_ID=

all:run

run:tidy
	go run $(GO_SRC) $(GROUP_ID)
tidy:
	go mod tidy
clean:
	rm -f -r ./tmp/
	rm -f -r ./pdftmp/
PHONY: all run tidy clean