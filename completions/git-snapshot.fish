complete -c git-snapshot -f
complete -c git-snapshot -n '__fish_use_subcommand' -a 'create list show diff verify restore drop delete config help version'
for c in create list show diff verify restore drop delete
    complete -c git-snapshot -n "__fish_seen_subcommand_from $c" -l repo -r -F -d 'Repository path'
    complete -c git-snapshot -n "__fish_seen_subcommand_from $c" -l ref -r -l namespace -r -l base -r -l message-template -r -l signing-key -r -l retention -r -l lock-timeout -r
    complete -c git-snapshot -n "__fish_seen_subcommand_from $c" -l include-untracked -l no-include-untracked -l include-ignored -l no-include-ignored -l create-reflog -l no-create-reflog -l sign -l no-sign -l yes -l json -l quiet -l verbose
    complete -c git-snapshot -n "__fish_seen_subcommand_from $c" -l output-format -r -a 'human json' -l color -r -a 'auto always never'
    complete -c git-snapshot -n "__fish_seen_subcommand_from $c" -l config-file -r -F -l restore-destination -r -F
end
complete -c git-snapshot -n '__fish_seen_subcommand_from create' -l message -r -l message-file -r -F -l allow-in-progress -l allow-dirty-submodules -l dry-run
complete -c git-snapshot -n '__fish_seen_subcommand_from list' -l limit -r -l reverse -l format -r -l show-ref -l show-base -l show-size
complete -c git-snapshot -n '__fish_seen_subcommand_from show' -l stat -l name-only -l patch -l format -r -l allow-unreachable -a latest
complete -c git-snapshot -n '__fish_seen_subcommand_from diff' -l stat -l name-only -l patch -l allow-unreachable -a 'latest HEAD worktree'
complete -c git-snapshot -n '__fish_seen_subcommand_from verify' -l allow-unreachable -a latest
complete -c git-snapshot -n '__fish_seen_subcommand_from restore' -l destination -r -F -l worktree -l overwrite -l force -l staged -l dry-run -l allow-unreachable -a latest
complete -c git-snapshot -n '__fish_seen_subcommand_from drop' -l count -r
complete -c git-snapshot -n '__fish_seen_subcommand_from config' -a 'get set unset list'
complete -c git-snapshot -n '__fish_seen_subcommand_from config' -l repo -r -F -l file -r -F -l global -l local -l effective -l show-origin -l json
