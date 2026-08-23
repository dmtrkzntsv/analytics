# Analytics

```sql active_projects
select alias, name
from analytics.projects
where archived_at is null
order by name
```

```sql archived_projects
select alias, name
from analytics.projects
where archived_at is not null
order by name
```

## Projects

{#each active_projects as p}

- [{p.name} — web](/web/{p.alias}) · [product](/product/{p.alias})

{/each}

{#if archived_projects.length > 0}

### Archived

{#each archived_projects as p}

- [{p.name} — web](/web/{p.alias}) · [product](/product/{p.alias})

{/each}

{/if}
