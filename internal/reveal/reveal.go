// Package reveal 은 파일 탐색기(macOS Finder / Windows 탐색기)로 폴더를 연다.
//
// 설정 창의 "폴더 열기"가 쓴다. 문제를 신고할 때 로그 파일을 찾아 첨부하려면 경로를 글자로
// 보여주는 것만으로는 부족하다 — 붙여넣기 없이 바로 열려야 한다.
//
// Wails의 BrowserOpenURL은 쓰지 않는다. 내부적으로 기본 브라우저를 여는 경로라
// file:// 주소를 넘기면 탐색기가 아니라 웹 브라우저가 뜰 수 있고, Windows 구현은 브라우저
// 실행 파일 목록으로 폴백까지 한다.
package reveal

// Folder opens dir in the OS file manager. 경로가 비었거나 플랫폼이 지원하지 않으면
// 아무 일도 하지 않는다. 실패는 에러로 돌려주되, 호출부는 로그만 남기면 된다
// (폴더가 안 열려도 경로는 화면에 글자로 남아 있다).
func Folder(dir string) error { return openFolder(dir) }
