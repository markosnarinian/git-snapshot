_git_snapshot()
{
    local cur prev cmd common
    COMPREPLY=()
    cur=${COMP_WORDS[COMP_CWORD]}
    prev=${COMP_WORDS[COMP_CWORD-1]}
    cmd=${COMP_WORDS[1]}
    common='--repo --ref --namespace --base --include-untracked --no-include-untracked --include-ignored --no-include-ignored --create-reflog --no-create-reflog --message-template --sign --no-sign --signing-key --output-format --color --yes --restore-destination --retention --lock-timeout --config-file --json --quiet --verbose --help'
    case "$prev" in
        --repo|--config-file|--message-file|--destination|--restore-destination|--file) COMPREPLY=( $(compgen -f -- "$cur") ); return ;;
        --output-format) COMPREPLY=( $(compgen -W 'human json' -- "$cur") ); return ;;
        --color) COMPREPLY=( $(compgen -W 'auto always never' -- "$cur") ); return ;;
    esac
    if (( COMP_CWORD == 1 )); then COMPREPLY=( $(compgen -W 'create list show diff verify restore drop delete config help version --version' -- "$cur") ); return; fi
    case "$cmd" in
        create) common="$common --message --message-file --allow-in-progress --allow-dirty-submodules --dry-run" ;;
        list) common="$common --limit --reverse --format --show-ref --show-base --show-size" ;;
        show) common="$common --stat --name-only --patch --format --allow-unreachable latest" ;;
        diff) common="$common --stat --name-only --patch --allow-unreachable latest HEAD worktree" ;;
        verify) common="$common --allow-unreachable latest" ;;
        restore) common="$common --destination --worktree --overwrite --force --staged --dry-run --allow-unreachable latest" ;;
        drop) common="$common --count" ;;
        config) common='get set unset list --repo --file --global --local --effective --show-origin --json --help' ;;
    esac
    COMPREPLY=( $(compgen -W "$common" -- "$cur") )
}
complete -F _git_snapshot git-snapshot
complete -F _git_snapshot 'git-snapshot'
